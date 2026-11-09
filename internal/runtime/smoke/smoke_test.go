package smoke_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/adapters/agent"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/adapters/fake"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/smoke"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"github.com/google/uuid"
)

var (
	_ smoke.Executor = (*agent.Client)(nil)
	_ smoke.Executor = (*fake.Agent)(nil)
)

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type stubSecrets struct {
	mu sync.Mutex
	n  int
}

func (s *stubSecrets) NewToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return fmt.Sprintf("token-%d", s.n), nil
}
func (*stubSecrets) Seal(p []byte) ([]byte, error) { return append([]byte("sealed:"), p...), nil }
func (*stubSecrets) Open(c []byte) ([]byte, error) {
	if !bytes.HasPrefix(c, []byte("sealed:")) {
		return nil, errors.New("not sealed")
	}
	return bytes.Clone(c[len("sealed:"):]), nil
}
func (*stubSecrets) NewHostKey() ([]byte, string, error) {
	return []byte("priv"), "ssh-ed25519 AAAA", nil
}
func (*stubSecrets) IssueServerCert(ip string) ([]byte, []byte, error) {
	return []byte("cert-" + ip), []byte("key-" + ip), nil
}

type job struct {
	job        usecase.Job
	status     string // queued | running | done
	nextRun    time.Time
	owner      string
	leaseUntil time.Time
}

type envRec struct {
	env     domain.Environment
	secrets usecase.SealedSecrets
}

// store — минимальные in-memory EnvironmentRepository и JobQueue с семантикой адаптера Postgres
// (копия по смыслу usecase/memstore_test.go, который недоступен из другого пакета).
type store struct {
	mu   sync.Mutex
	envs map[uuid.UUID]*envRec
	jobs []*job
	seq  int64
}

var (
	_ usecase.EnvironmentRepository = (*store)(nil)
	_ usecase.JobQueue              = (*store)(nil)
)

func newStore() *store { return &store{envs: map[uuid.UUID]*envRec{}} }

func (m *store) addJob(sid uuid.UUID, kind usecase.JobKind) {
	m.seq++
	m.jobs = append(m.jobs, &job{job: usecase.Job{ID: m.seq, SessionID: sid, Kind: kind}, status: "queued", nextRun: time.Now()})
}

func (m *store) active(sid uuid.UUID) *job {
	for _, j := range m.jobs {
		if j.job.SessionID == sid && j.status != "done" {
			return j
		}
	}
	return nil
}

func (m *store) CreateWithJob(_ context.Context, env domain.Environment, maxActive int) (domain.Environment, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.envs[env.SessionID]; ok {
		return e.env, false, nil
	}
	active := 0
	for _, e := range m.envs {
		if !e.env.State.Finished() {
			active++
		}
	}
	if active >= maxActive {
		return domain.Environment{}, false, usecase.ErrCapacity
	}
	env.Version, env.CreatedAt, env.UpdatedAt = 1, time.Now(), time.Now()
	m.envs[env.SessionID] = &envRec{env: env}
	m.addJob(env.SessionID, usecase.JobCreate)
	return env, true, nil
}

func (m *store) Get(_ context.Context, id uuid.UUID) (domain.Environment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.envs[id]
	if !ok {
		return domain.Environment{}, usecase.ErrNotFound
	}
	return e.env, nil
}

func (m *store) MarkForDelete(_ context.Context, id uuid.UUID, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.envs[id]
	if !ok {
		return usecase.ErrNotFound
	}
	if e.env.State.Finished() {
		return nil
	}
	e.env.DesiredState = domain.StateDeleted
	e.env.Version++
	if j := m.active(id); j != nil {
		j.job.Kind = usecase.JobDelete
		return nil
	}
	m.addJob(id, usecase.JobDelete)
	return nil
}

func (m *store) owns(sid uuid.UUID, owner string) bool {
	for _, j := range m.jobs {
		if j.job.SessionID == sid && j.status == "running" && j.owner == owner {
			return true
		}
	}
	return false
}

func (m *store) Save(_ context.Context, env domain.Environment, owner string) (domain.Environment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.envs[env.SessionID]
	if !ok || e.env.Version != env.Version || !m.owns(env.SessionID, owner) {
		return domain.Environment{}, usecase.ErrStale
	}
	if e.env.DesiredState == domain.StateDeleted {
		env.DesiredState = domain.StateDeleted
	}
	env.CreatedAt, env.Version, env.UpdatedAt = e.env.CreatedAt, e.env.Version+1, time.Now()
	e.env = env
	return env, nil
}

func (m *store) SaveSecrets(_ context.Context, id uuid.UUID, s usecase.SealedSecrets, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.envs[id]
	if !ok || !m.owns(id, owner) {
		return usecase.ErrStale
	}
	e.secrets = s
	return nil
}

func (m *store) LoadSecrets(_ context.Context, id uuid.UUID) (usecase.SealedSecrets, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.envs[id]
	if !ok {
		return usecase.SealedSecrets{}, usecase.ErrNotFound
	}
	return e.secrets, nil
}

func (m *store) KnownSessions(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]domain.State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[uuid.UUID]domain.State{}
	for _, id := range ids {
		if e, ok := m.envs[id]; ok {
			out[id] = e.env.State
		}
	}
	return out, nil
}

func (m *store) ListByState(_ context.Context, state domain.State) ([]domain.Environment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Environment
	for _, e := range m.envs {
		if e.env.State == state {
			out = append(out, e.env)
		}
	}
	return out, nil
}

func (m *store) Claim(_ context.Context, owner string, lease time.Duration) (usecase.Job, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for _, j := range m.jobs {
		if j.status != "done" && !j.nextRun.After(now) && (j.leaseUntil.IsZero() || j.leaseUntil.Before(now)) {
			j.status, j.owner, j.leaseUntil = "running", owner, now.Add(lease)
			j.job.Attempt++
			return j.job, true, nil
		}
	}
	return usecase.Job{}, false, nil
}

func (m *store) owned(job usecase.Job, owner string) *job {
	for _, j := range m.jobs {
		if j.job.ID == job.ID && j.owner == owner && j.status == "running" {
			return j
		}
	}
	return nil
}

func (m *store) Renew(_ context.Context, jb usecase.Job, owner string, lease time.Duration) (usecase.JobKind, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.owned(jb, owner)
	if j == nil {
		return "", usecase.ErrLeaseLost
	}
	j.leaseUntil = time.Now().Add(lease)
	return j.job.Kind, nil
}

func (m *store) Complete(_ context.Context, jb usecase.Job, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.owned(jb, owner)
	if j == nil || j.job.Kind != jb.Kind {
		return usecase.ErrLeaseLost
	}
	j.status = "done"
	return nil
}

func (m *store) Retry(_ context.Context, jb usecase.Job, owner string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.owned(jb, owner)
	if j == nil {
		return usecase.ErrLeaseLost
	}
	j.status, j.nextRun, j.owner, j.leaseUntil = "queued", at, "", time.Time{}
	return nil
}

func setup() (smoke.Deps, *fake.Cloud, *fake.Host) {
	st := newStore()
	cloud, host, ag := fake.New(20 * time.Millisecond)
	sec := &stubSecrets{}
	svc := usecase.NewService(st, usecase.Settings{
		Enabled: true, MaxActive: 10, AgentVersion: "dev", Profiles: map[string]bool{"default": true},
	})
	return smoke.Deps{
		Service: svc, Repo: st, Secrets: sec, Agent: ag, Cloud: cloud,
		NewWorker: func(owner string, lease time.Duration) *usecase.Worker {
			return usecase.NewWorker(st, nil, st, cloud, host, ag, sec, realClock{}, usecase.WorkerSettings{
				Owner: owner, Concurrency: 1, Lease: lease, PollInterval: 10 * time.Millisecond, EnvName: "smoke-test",
			})
		},
	}, cloud, host
}

func TestSmokeFakeHappyPath(t *testing.T) {
	deps, _, _ := setup()
	rep, err := smoke.Run(context.Background(), deps, smoke.Options{Language: "python", Parallel: 2, Timeout: 30 * time.Second})
	if err != nil || !rep.OK {
		t.Fatalf("err=%v report=%+v", err, rep)
	}
	if len(rep.Sessions) != 2 || rep.Leftovers != 0 || !rep.TokensDistinct {
		t.Fatalf("report=%+v", rep)
	}
	for _, s := range rep.Sessions {
		if s.FinalState != string(domain.StateDeleted) || !s.Print42 || !s.AgentSession {
			t.Fatalf("session=%+v", s)
		}
	}
}

func TestSmokeKillWorkerAfterVolume(t *testing.T) {
	deps, cloud, _ := setup()
	rep, err := smoke.Run(context.Background(), deps, smoke.Options{
		Language: "python", KillWorkerAfter: smoke.KillAfterVolume, Timeout: 30 * time.Second, Lease: 100 * time.Millisecond,
	})
	if err != nil || !rep.OK {
		t.Fatalf("err=%v report=%+v", err, rep)
	}
	if !rep.Sessions[0].WorkerKilled || rep.Leftovers != 0 || cloud.Count("volume") != 0 {
		t.Fatalf("report=%+v volumes=%d", rep, cloud.Count("volume"))
	}
}

func TestSmokeRejectsBadOptions(t *testing.T) {
	deps, _, _ := setup()
	if _, err := smoke.Run(context.Background(), deps, smoke.Options{Language: "go"}); err == nil {
		t.Fatal("expected language error")
	}
	if _, err := smoke.Run(context.Background(), deps, smoke.Options{Language: "r", KillWorkerAfter: "port"}); err == nil {
		t.Fatal("expected kill-worker-after error")
	}
}

// failingExec возвращает FAILED для любого выполнения.
type failingExec struct{ smoke.Executor }

func (failingExec) Execution(context.Context, usecase.AgentEndpoint, string) (string, error) {
	return "FAILED", nil
}

func TestSmokeExecutionFailedCleansUp(t *testing.T) {
	deps, cloud, _ := setup()
	deps.Agent = failingExec{deps.Agent}
	rep, err := smoke.Run(context.Background(), deps, smoke.Options{Language: "python", Timeout: 30 * time.Second, Lease: 100 * time.Millisecond})
	if err == nil || rep.OK {
		t.Fatalf("expected failure, report=%+v", rep)
	}
	s := rep.Sessions[0]
	if s.FinalState != string(domain.StateDeleted) || rep.Leftovers != 0 || cloud.Count("server") != 0 {
		t.Fatalf("report=%+v", rep)
	}
}

func TestSmokeTimeoutMidCreateCleansUp(t *testing.T) {
	deps, cloud, host := setup()
	host.Block = make(chan struct{}) // Configure не завершится
	rep, err := smoke.Run(context.Background(), deps, smoke.Options{Language: "python", Timeout: time.Second, Lease: 100 * time.Millisecond})
	if err == nil || rep.OK {
		t.Fatalf("expected failure, report=%+v", rep)
	}
	if rep.Leftovers != 0 || cloud.Count("server") != 0 || cloud.Count("volume") != 0 {
		t.Fatalf("report=%+v", rep)
	}
	if st := rep.Sessions[0].FinalState; st != string(domain.StateDeleted) && st != string(domain.StateFailed) {
		t.Fatalf("report=%+v", rep)
	}
}

func TestSmokeKillWorkerAfterServer(t *testing.T) {
	deps, cloud, host := setup()
	// Configure держит первого воркера после сохранения ServerID, пока его не отменит smoke;
	// затем блок снимается, и второй воркер завершает создание.
	host.Block = make(chan struct{})
	time.AfterFunc(500*time.Millisecond, func() { close(host.Block) })
	rep, err := smoke.Run(context.Background(), deps, smoke.Options{
		Language: "python", KillWorkerAfter: smoke.KillAfterServer, Timeout: 30 * time.Second, Lease: 100 * time.Millisecond,
	})
	if err != nil || !rep.OK {
		t.Fatalf("err=%v report=%+v", err, rep)
	}
	if !rep.Sessions[0].WorkerKilled || rep.Leftovers != 0 || cloud.Count("server") != 0 {
		t.Fatalf("report=%+v", rep)
	}
}
