package usecase_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"github.com/google/uuid"
)

// testClock — управляемые часы: Now возвращает t и сдвигает его на step.
type testClock struct {
	mu   sync.Mutex
	t    time.Time
	step time.Duration
}

func newClock() *testClock {
	return &testClock{t: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.t
	c.t = c.t.Add(c.step)
	return now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type memJob struct {
	job        usecase.Job
	status     string // queued | running | done
	nextRun    time.Time
	owner      string
	leaseUntil time.Time
}

type memEnv struct {
	env     domain.Environment
	secrets usecase.SealedSecrets
	history []domain.State
}

// memStore — in-memory EnvironmentRepository и JobQueue с той же семантикой, что у адаптера
// Postgres: Save проверяет версию и владение running-заданием, не снимает desired_state=DELETED;
// MarkForDelete поднимает версию и переводит активное задание в delete; Complete сверяет kind;
// Claim берёт задание с наступившим next_run_at и истёкшим lease (по testClock).
type memStore struct {
	mu       sync.Mutex
	clock    *testClock
	envs     map[uuid.UUID]*memEnv
	jobs     []*memJob
	seq      int64
	saveHook func(domain.Environment) error
	// renewErr / completeErr — ошибка БД (не ErrLeaseLost) для Renew / Complete; nil — штатно.
	renewErr    error
	completeErr error
	renewCalls  int
	pool        []*domain.PoolVPS
	reserves    int
}

var (
	_ usecase.EnvironmentRepository = (*memStore)(nil)
	_ usecase.JobQueue              = (*memStore)(nil)
)

func newMemStore(clock *testClock) *memStore {
	return &memStore{clock: clock, envs: map[uuid.UUID]*memEnv{}}
}

func (m *memStore) addJobLocked(sid uuid.UUID, kind usecase.JobKind) {
	m.seq++
	m.jobs = append(m.jobs, &memJob{
		job:     usecase.Job{ID: m.seq, SessionID: sid, Kind: kind},
		status:  "queued",
		nextRun: m.clock.Now(),
	})
}

func (m *memStore) activeJobLocked(sid uuid.UUID) *memJob {
	for _, j := range m.jobs {
		if j.job.SessionID == sid && j.status != "done" {
			return j
		}
	}
	return nil
}

func (m *memStore) CreateWithJob(_ context.Context, env domain.Environment, maxActive int) (domain.Environment, bool, error) {
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
	now := m.clock.Now()
	env.Version, env.CreatedAt, env.UpdatedAt = 1, now, now
	m.envs[env.SessionID] = &memEnv{env: env, history: []domain.State{env.State}}
	m.addJobLocked(env.SessionID, usecase.JobCreate)
	return env, true, nil
}

func (m *memStore) Get(_ context.Context, id uuid.UUID) (domain.Environment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.envs[id]
	if !ok {
		return domain.Environment{}, usecase.ErrNotFound
	}
	return e.env, nil
}

func (m *memStore) MarkForDelete(_ context.Context, id uuid.UUID, _ string) error {
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
	if j := m.activeJobLocked(id); j != nil {
		j.job.Kind = usecase.JobDelete
		return nil
	}
	m.addJobLocked(id, usecase.JobDelete)
	return nil
}

func (m *memStore) ownsLocked(sid uuid.UUID, owner string) bool {
	for _, j := range m.jobs {
		if j.job.SessionID == sid && j.status == "running" && j.owner == owner {
			return true
		}
	}
	return false
}

func (m *memStore) Save(_ context.Context, env domain.Environment, owner string) (domain.Environment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveHook != nil {
		if err := m.saveHook(env); err != nil {
			return domain.Environment{}, err
		}
	}
	e, ok := m.envs[env.SessionID]
	if !ok || e.env.Version != env.Version || !m.ownsLocked(env.SessionID, owner) {
		return domain.Environment{}, usecase.ErrStale
	}
	if e.env.DesiredState == domain.StateDeleted {
		env.DesiredState = domain.StateDeleted
	}
	env.CreatedAt = e.env.CreatedAt
	env.Version = e.env.Version + 1
	env.UpdatedAt = m.clock.Now()
	e.env = env
	if last := e.history[len(e.history)-1]; last != env.State {
		e.history = append(e.history, env.State)
	}
	return env, nil
}

func (m *memStore) SaveSecrets(_ context.Context, id uuid.UUID, s usecase.SealedSecrets, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.envs[id]
	if !ok || !m.ownsLocked(id, owner) {
		return usecase.ErrStale
	}
	e.secrets = usecase.SealedSecrets{
		Token: bytes.Clone(s.Token), HostPrivateKey: bytes.Clone(s.HostPrivateKey), HostPublicKey: s.HostPublicKey,
	}
	return nil
}

func (m *memStore) LoadSecrets(_ context.Context, id uuid.UUID) (usecase.SealedSecrets, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.envs[id]
	if !ok {
		return usecase.SealedSecrets{}, usecase.ErrNotFound
	}
	return e.secrets, nil
}

func (m *memStore) KnownSessions(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]domain.State, error) {
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

func (m *memStore) ListByState(_ context.Context, state domain.State) ([]domain.Environment, error) {
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

func (m *memStore) Claim(_ context.Context, owner string, lease time.Duration) (usecase.Job, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock.Now()
	var ready []*memJob
	for _, j := range m.jobs {
		if j.status != "done" && !j.nextRun.After(now) && (j.leaseUntil.IsZero() || j.leaseUntil.Before(now)) {
			ready = append(ready, j)
		}
	}
	if len(ready) == 0 {
		return usecase.Job{}, false, nil
	}
	sort.SliceStable(ready, func(a, b int) bool { return ready[a].nextRun.Before(ready[b].nextRun) })
	j := ready[0]
	j.status, j.owner, j.leaseUntil = "running", owner, now.Add(lease)
	j.job.Attempt++
	return j.job, true, nil
}

func (m *memStore) ownedJobLocked(job usecase.Job, owner string) *memJob {
	for _, j := range m.jobs {
		if j.job.ID == job.ID && j.owner == owner && j.status == "running" {
			return j
		}
	}
	return nil
}

func (m *memStore) Renew(_ context.Context, job usecase.Job, owner string, lease time.Duration) (usecase.JobKind, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.renewCalls++
	if m.renewErr != nil {
		return "", m.renewErr
	}
	j := m.ownedJobLocked(job, owner)
	if j == nil {
		return "", usecase.ErrLeaseLost
	}
	j.leaseUntil = m.clock.Now().Add(lease)
	return j.job.Kind, nil
}

func (m *memStore) Complete(_ context.Context, job usecase.Job, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.completeErr; err != nil {
		m.completeErr = nil
		return err
	}
	j := m.ownedJobLocked(job, owner)
	if j == nil || j.job.Kind != job.Kind {
		return usecase.ErrLeaseLost
	}
	j.status = "done"
	return nil
}

func (m *memStore) Retry(_ context.Context, job usecase.Job, owner string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.ownedJobLocked(job, owner)
	if j == nil {
		return usecase.ErrLeaseLost
	}
	j.status, j.nextRun, j.owner, j.leaseUntil = "queued", at, "", time.Time{}
	return nil
}

// --- помощники тестов ---

// job возвращает копию последнего задания сессии.
func (m *memStore) job(sid uuid.UUID) (memJob, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.jobs) - 1; i >= 0; i-- {
		if m.jobs[i].job.SessionID == sid {
			return *m.jobs[i], true
		}
	}
	return memJob{}, false
}

// steal передаёт running-задание сессии другому владельцу (перехват lease другой репликой).
func (m *memStore) steal(sid uuid.UUID, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j := m.activeJobLocked(sid); j != nil && j.status == "running" {
		j.owner = owner
		return nil
	}
	return errors.New("no running job")
}

func (m *memStore) history(sid uuid.UUID) []domain.State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]domain.State(nil), m.envs[sid].history...)
}

func (m *memStore) setSaveHook(h func(domain.Environment) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saveHook = h
}

func (m *memStore) setRenewErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.renewErr = err
}

func (m *memStore) failCompleteOnce(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completeErr = err
}

func (m *memStore) renews() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.renewCalls
}

var _ usecase.PoolRepository = (*memStore)(nil)

// Пул в памяти: та же семантика, что у адаптера Postgres (ReservePool считает PREPARING+READY текущих
// образа и версии, ClaimPool переносит VPS в окружение QUEUED атомарно, ListPool — старые первыми).

func (m *memStore) ReservePool(_ context.Context, size int, imageID, agentVersion string) ([]domain.PoolVPS, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	have := 0
	for _, p := range m.pool {
		if (p.State == domain.PoolPreparing || p.State == domain.PoolReady) && p.ImageID == imageID && p.AgentVersion == agentVersion {
			have++
		}
	}
	out := make([]domain.PoolVPS, 0, max(size-have, 0))
	for range size - have {
		now := m.clock.Now()
		p := &domain.PoolVPS{ID: uuid.New(), State: domain.PoolPreparing, ImageID: imageID, AgentVersion: agentVersion,
			CreatedAt: now, UpdatedAt: now}
		m.pool = append(m.pool, p)
		out = append(out, *p)
	}
	m.reserves++
	return out, nil
}

func (m *memStore) poolLocked(id uuid.UUID) *domain.PoolVPS {
	for _, p := range m.pool {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (m *memStore) SavePreparing(_ context.Context, v domain.PoolVPS) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.poolLocked(v.ID)
	if p == nil || p.State != domain.PoolPreparing {
		return usecase.ErrStale
	}
	p.State, p.Resources, p.HostPrivateKey, p.HostPublicKey = v.State, v.Resources, bytes.Clone(v.HostPrivateKey), v.HostPublicKey
	p.UpdatedAt = m.clock.Now()
	return nil
}

func (m *memStore) ClaimPool(_ context.Context, env domain.Environment, owner, imageID, agentVersion string) (domain.Environment, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var p *domain.PoolVPS
	for _, c := range m.pool {
		if c.State == domain.PoolReady && c.ImageID == imageID && c.AgentVersion == agentVersion {
			p = c
			break
		}
	}
	if p == nil {
		return env, false, nil
	}
	e, ok := m.envs[env.SessionID]
	if !ok || e.env.Version != env.Version || e.env.State != domain.StateQueued || !m.ownsLocked(env.SessionID, owner) {
		return env, false, usecase.ErrStale
	}
	p.State, p.SessionID = domain.PoolClaimed, env.SessionID
	e.env.State, e.env.PoolID, e.env.Resources = domain.StateVerifying, p.ID, p.Resources
	e.env.Version++
	e.secrets.HostPrivateKey, e.secrets.HostPublicKey = bytes.Clone(p.HostPrivateKey), p.HostPublicKey
	e.history = append(e.history, domain.StateVerifying)
	return e.env, true, nil
}

func (m *memStore) MarkPoolDeleting(_ context.Context, id uuid.UUID, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.poolLocked(id); p != nil && p.State != domain.PoolDeleting {
		p.State, p.LastError = domain.PoolDeleting, reason
	}
	return nil
}

func (m *memStore) ListPool(context.Context) ([]domain.PoolVPS, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.PoolVPS, 0, len(m.pool))
	for _, p := range m.pool {
		out = append(out, *p)
	}
	return out, nil
}

func (m *memStore) DeletePool(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pool = slices.DeleteFunc(m.pool, func(p *domain.PoolVPS) bool { return p.ID == id })
	return nil
}

// poolStates возвращает состояния VPS пула по порядку создания.
func (m *memStore) poolStates() []domain.PoolState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.PoolState, 0, len(m.pool))
	for _, p := range m.pool {
		out = append(out, p.State)
	}
	return out
}

// addPool добавляет VPS пула в обход подготовки.
func (m *memStore) addPool(p domain.PoolVPS) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = m.clock.Now()
	}
	m.pool = append(m.pool, &p)
}
