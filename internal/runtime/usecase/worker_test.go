package usecase_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/adapters/fake"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"github.com/google/uuid"
)

const testLease = 30 * time.Millisecond

// stubSecrets — детерминированные секреты: Seal добавляет префикс, ключи и сертификаты — заглушки.
type stubSecrets struct {
	mu            sync.Mutex
	tokens, hosts int
}

const sealPrefix = "sealed:"

func (s *stubSecrets) NewToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens++
	return fmt.Sprintf("token-%d", s.tokens), nil
}

func (s *stubSecrets) Seal(p []byte) ([]byte, error) {
	return append([]byte(sealPrefix), p...), nil
}

func (s *stubSecrets) Open(c []byte) ([]byte, error) {
	if !bytes.HasPrefix(c, []byte(sealPrefix)) {
		return nil, errors.New("not sealed")
	}
	return bytes.Clone(c[len(sealPrefix):]), nil
}

func (s *stubSecrets) NewHostKey() ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hosts++
	return []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nAAAAprivate\n-----END OPENSSH PRIVATE KEY-----\n"),
		"ssh-ed25519 AAAApublic", nil
}

func (s *stubSecrets) IssueServerCert(ip string) ([]byte, []byte, error) {
	return []byte("cert-" + ip), []byte("key-" + ip), nil
}

func (s *stubSecrets) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens, s.hosts
}

// recordingCloud запоминает user_data последнего EnsureServer. hang (если задан до запуска
// воркера) — методы, которые блокируются до отмены ctx.
type recordingCloud struct {
	*fake.Cloud
	mu       sync.Mutex
	userData string
	hang     map[string]bool
	// afterList, если задан, вызывается после ListOwned (до ответа).
	afterList func()
}

func (c *recordingCloud) EnsureServer(ctx context.Context, env domain.Environment, userData string) (string, error) {
	c.mu.Lock()
	c.userData = userData
	c.mu.Unlock()
	if c.hang["EnsureServer"] {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return c.Cloud.EnsureServer(ctx, env, userData)
}

func (c *recordingCloud) ListOwned(ctx context.Context) ([]usecase.CloudResource, error) {
	if c.hang["ListOwned"] {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	owned, err := c.Cloud.ListOwned(ctx)
	if c.afterList != nil {
		c.afterList()
	}
	return owned, err
}

// hangingAssign — агент, чей /assign не отвечает до отмены ctx.
type hangingAssign struct {
	*fake.Agent
	calls atomic.Int32
}

func (a *hangingAssign) Assign(ctx context.Context, _ usecase.AgentEndpoint, _ uuid.UUID, _ string) (usecase.AgentHealth, int, error) {
	a.calls.Add(1)
	<-ctx.Done()
	return usecase.AgentHealth{}, 0, ctx.Err()
}

// hostFunc — HostConfigurator из функции.
type hostFunc func(ctx context.Context, c usecase.HostConfig) error

func (f hostFunc) Configure(ctx context.Context, c usecase.HostConfig) error { return f(ctx, c) }

type harness struct {
	t     *testing.T
	clock *testClock
	store *memStore
	cloud *recordingCloud
	host  *fake.Host
	agent *fake.Agent
	ac    usecase.AgentClient // по умолчанию agent
	sec   *stubSecrets
	svc   *usecase.Service
	cfg   usecase.HostConfigurator // по умолчанию host
	s     usecase.WorkerSettings
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clock := newClock()
	store := newMemStore(clock)
	cloud, host, agent := fake.New(0)
	return &harness{
		t: t, clock: clock, store: store, cloud: &recordingCloud{Cloud: cloud}, host: host, agent: agent,
		ac: agent, sec: &stubSecrets{},
		svc: usecase.NewService(store, usecase.Settings{
			Enabled: true, MaxActive: 10, AgentVersion: "0.1.0", Profiles: map[string]bool{"default": true},
		}),
		cfg: host,
		s: usecase.WorkerSettings{
			Owner: "w1", Concurrency: 2, Lease: testLease, PollInterval: 5 * time.Millisecond,
			ReconcileInterval: time.Hour, StartupTimeout: 20 * time.Minute, AnsibleTimeout: time.Minute,
			DrainTimeout: time.Second, DeleteTimeout: time.Minute,
			PythonImage: "py:1", KernelDNS: "1.1.1.1", BlockedPublicIPs: []string{"203.0.113.1"},
			EnvName: "test", ImageID: "img-1", AgentVersion: "0.1.0",
		},
	}
}

func (h *harness) worker(owner string) *usecase.Worker {
	s := h.s
	s.Owner = owner
	w := usecase.NewWorker(h.store, h.store, h.store, h.cloud, h.cfg, h.ac, h.sec, h.clock, s)
	usecase.SetVerifyInterval(w, 2*time.Millisecond)
	return w
}

func (h *harness) request() uuid.UUID {
	h.t.Helper()
	sid := uuid.New()
	if _, err := h.svc.RequestCreate(context.Background(), usecase.CreateEnvironment{SessionID: sid, Language: "python"}); err != nil {
		h.t.Fatal(err)
	}
	return sid
}

func (h *harness) env(sid uuid.UUID) domain.Environment {
	h.t.Helper()
	env, err := h.store.Get(context.Background(), sid)
	if err != nil {
		h.t.Fatal(err)
	}
	return env
}

func runOnce(t *testing.T, w *usecase.Worker) (bool, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return w.RunOnce(ctx)
}

// ready создаёт окружение и доводит его до READY.
func (h *harness) ready() uuid.UUID {
	h.t.Helper()
	sid := h.request()
	if ok, err := runOnce(h.t, h.worker("w1")); !ok || err != nil {
		h.t.Fatalf("RunOnce = %v, %v", ok, err)
	}
	if env := h.env(sid); env.State != domain.StateReady {
		h.t.Fatalf("state = %s, last_error = %q", env.State, env.LastError)
	}
	return sid
}

func (h *harness) resources() int {
	return h.cloud.Count("server") + h.cloud.Count("volume") + h.cloud.Count("port")
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCreateHappyPath(t *testing.T) {
	h := newHarness(t)
	sid := h.ready()
	env := h.env(sid)
	r := env.Resources
	if r.ServerID == "" || r.VolumeID == "" || r.PortID == "" || r.PrivateIP == "" {
		t.Fatalf("resources = %+v", r)
	}
	if want := "https://" + r.PrivateIP + ":8443"; env.Endpoint != want {
		t.Fatalf("endpoint = %q, want %q", env.Endpoint, want)
	}
	if j, _ := h.store.job(sid); j.status != "done" {
		t.Fatalf("job status = %s", j.status)
	}
	if got := h.store.history(sid); fmt.Sprint(got) != "[QUEUED CREATING CONFIGURING VERIFYING READY]" {
		t.Fatalf("history = %v", got)
	}
	ud := h.cloud.userData
	for _, want := range []string{"#cloud-config", "ssh_deletekeys: true", "ssh_keys:", "ed25519_private: |",
		"    -----BEGIN OPENSSH PRIVATE KEY-----", `ed25519_public: "ssh-ed25519 AAAApublic"`} {
		if !strings.Contains(ud, want) {
			t.Fatalf("user_data has no %q:\n%s", want, ud)
		}
	}
	c := h.host.Last()
	if c.Name != sid || c.PrivateIP != r.PrivateIP ||
		c.HostPublicKey != "ssh-ed25519 AAAApublic" || string(c.ServerCertPEM) != "cert-"+r.PrivateIP ||
		c.PythonImage != "py:1" || c.KernelDNS != "1.1.1.1" || len(c.BlockedPublicIPs) != 1 {
		t.Fatalf("host config = %+v", c)
	}
	sec, _ := h.store.LoadSecrets(context.Background(), sid)
	if string(sec.Token) != "sealed:token-1" || !bytes.HasPrefix(sec.HostPrivateKey, []byte(sealPrefix)) {
		t.Fatalf("secrets not sealed: %+v", sec)
	}
	if ok, err := runOnce(t, h.worker("w1")); ok || err != nil {
		t.Fatalf("no jobs expected, got %v %v", ok, err)
	}
}

func TestCreateIdempotentSteps(t *testing.T) {
	h := newHarness(t)
	h.s.StartupTimeout = 100 * time.Hour
	sid := h.request()
	// Каждое новое сохранение падает один раз: шаг выполняется, но его результат теряется,
	// и следующий RunOnce повторяет тот же шаг.
	seen := map[string]bool{}
	h.store.setSaveHook(func(e domain.Environment) error {
		key := fmt.Sprintf("%s|%+v|%s", e.State, e.Resources, e.Endpoint)
		if seen[key] {
			return nil
		}
		seen[key] = true
		return errors.New("db is down")
	})
	w := h.worker("w1")
	for i := 0; h.env(sid).State != domain.StateReady; i++ {
		if i > 40 {
			t.Fatalf("not READY after %d runs: %+v", i, h.env(sid))
		}
		if ok, _ := runOnce(t, w); !ok {
			h.clock.Advance(11 * time.Minute)
		}
	}
	for _, kind := range []string{"server", "volume", "port"} {
		if n := h.cloud.Count(kind); n != 1 {
			t.Fatalf("Count(%s) = %d", kind, n)
		}
	}
	for _, m := range []string{"EnsurePort", "EnsureVolume", "EnsureServer"} {
		if h.cloud.Calls(m) < 2 {
			t.Fatalf("%s called %d times, want each step repeated", m, h.cloud.Calls(m))
		}
	}
	if tokens, hosts := h.sec.counts(); tokens != 1 || hosts != 1 {
		t.Fatalf("secrets generated tokens=%d hosts=%d", tokens, hosts)
	}
}

func TestCreateRechecksKnownResources(t *testing.T) {
	for _, method := range []string{"EnsureVolume", "EnsureServer"} {
		t.Run(method, func(t *testing.T) {
			h := newHarness(t)
			h.s.StartupTimeout = 100 * time.Hour
			sid := h.request()
			// Ресурс создан, ID получен, но ожидание готовности оборвалось временной ошибкой.
			h.cloud.FailAfterOnce(method, fmt.Errorf("%w: wait", usecase.ErrCloudTransient))
			w := h.worker("w1")
			_, _ = runOnce(t, w)
			if env := h.env(sid); env.State != domain.StateCreating {
				t.Fatalf("state = %s, want CREATING", env.State)
			}
			h.clock.Advance(time.Hour)
			if ok, err := runOnce(t, w); !ok || err != nil {
				t.Fatalf("RunOnce = %v %v", ok, err)
			}
			if got := h.cloud.Calls(method); got != 2 {
				t.Fatalf("%s calls = %d, want 2", method, got)
			}
			if env := h.env(sid); env.State != domain.StateReady {
				t.Fatalf("state = %s (%s)", env.State, env.LastError)
			}
		})
	}
}

func TestCreateResumesAfterLostVolumeID(t *testing.T) {
	h := newHarness(t)
	h.s.StartupTimeout = 100 * time.Hour
	sid := h.request()
	failed := false
	h.store.setSaveHook(func(e domain.Environment) error {
		if e.Resources.VolumeID != "" && !failed {
			failed = true
			return errors.New("connection reset")
		}
		return nil
	})
	_, _ = runOnce(t, h.worker("w1"))
	if !failed || h.env(sid).Resources.VolumeID != "" || h.cloud.Count("volume") != 1 {
		t.Fatalf("setup: failed=%v env=%+v", failed, h.env(sid))
	}
	h.clock.Advance(time.Hour) // и backoff, и lease первого воркера истекли
	if ok, err := runOnce(t, h.worker("w2")); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	if env := h.env(sid); env.State != domain.StateReady {
		t.Fatalf("state = %s (%s)", env.State, env.LastError)
	}
	if h.cloud.Count("volume") != 1 || h.cloud.Calls("EnsureVolume") != 2 {
		t.Fatalf("volumes = %d, EnsureVolume calls = %d", h.cloud.Count("volume"), h.cloud.Calls("EnsureVolume"))
	}
}

func TestCreateCancelledByDeleteDuringConfigure(t *testing.T) {
	h := newHarness(t)
	h.host.Block = make(chan struct{})
	defer close(h.host.Block)
	sid := h.request()
	done := make(chan error, 1)
	go func() { _, err := runOnce(t, h.worker("w1")); done <- err }()
	waitFor(t, "Configure", func() bool { return h.host.Calls() == 1 })

	if err := h.svc.RequestDelete(context.Background(), sid, "user closed session"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("RunOnce err = %v", err)
	}
	env := h.env(sid)
	if env.State != domain.StateDeleted || env.Endpoint != "" {
		t.Fatalf("env = %+v", env)
	}
	for _, s := range h.store.history(sid) {
		if s == domain.StateReady {
			t.Fatal("environment must never become READY")
		}
	}
	if n := h.resources(); n != 0 {
		t.Fatalf("resources left = %d", n)
	}
	if j, _ := h.store.job(sid); j.status != "done" || j.job.Kind != usecase.JobDelete {
		t.Fatalf("job = %+v", j)
	}
}

func TestLeaseLostStopsStep(t *testing.T) {
	h := newHarness(t)
	h.host.Block = make(chan struct{})
	defer close(h.host.Block)
	sid := h.request()
	done := make(chan error, 1)
	go func() { _, err := runOnce(t, h.worker("w1")); done <- err }()
	waitFor(t, "Configure", func() bool { return h.host.Calls() == 1 })

	if err := h.store.steal(sid, "w2"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, usecase.ErrLeaseLost) {
			t.Fatalf("err = %v, want ErrLeaseLost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("step was not cancelled after lease loss")
	}
	if env := h.env(sid); env.State != domain.StateConfiguring {
		t.Fatalf("state = %s, the old worker must not change it", env.State)
	}
	if j, _ := h.store.job(sid); j.status != "running" || j.owner != "w2" {
		t.Fatalf("job = %+v", j)
	}
	if h.resources() != 3 || h.cloud.Calls("DeleteServer") != 0 {
		t.Fatal("old worker must not touch resources")
	}
}

// agentHost настраивает VPS через fake Host, затем выполняет after (подмена ответа агента).
func agentHost(h *harness, after func(c usecase.HostConfig)) hostFunc {
	return func(ctx context.Context, c usecase.HostConfig) error {
		if err := h.host.Configure(ctx, c); err != nil {
			return err
		}
		after(c)
		return nil
	}
}

func runVerifyTimeout(t *testing.T, h *harness, sid uuid.UUID) {
	t.Helper()
	done := make(chan error, 1)
	go func() { _, err := runOnce(t, h.worker("w1")); done <- err }()
	waitFor(t, "assign attempts", func() bool { return h.agent.Calls("Assign") >= 3 })
	if s := h.env(sid).State; s != domain.StateVerifying {
		t.Fatalf("state while polling = %s", s)
	}
	h.clock.Advance(h.s.StartupTimeout)
	if err := <-done; err != nil {
		t.Fatalf("RunOnce err = %v", err)
	}
	env := h.env(sid)
	if env.State != domain.StateFailed || env.LastError == "" || env.Endpoint != "" {
		t.Fatalf("env = %+v", env)
	}
	hist := fmt.Sprint(h.store.history(sid))
	if !strings.HasSuffix(hist, "VERIFYING CLEANUP_PENDING FAILED]") {
		t.Fatalf("history = %s", hist)
	}
	if n := h.resources(); n != 0 {
		t.Fatalf("resources left = %d", n)
	}
	if j, _ := h.store.job(sid); j.status != "done" {
		t.Fatalf("job = %+v", j)
	}
}

func TestVerifyTimeoutCleansUp(t *testing.T) {
	h := newHarness(t)
	h.cfg = agentHost(h, func(c usecase.HostConfig) {
		h.agent.SetStatus("https://"+c.PrivateIP+":8443", 503)
	})
	sid := h.request()
	runVerifyTimeout(t, h, sid)
	if !strings.Contains(h.env(sid).LastError, "503") {
		t.Fatalf("last_error = %q", h.env(sid).LastError)
	}
}

// Kernel не стартовал: агент отвечает 503 с FAILED своей сессии — окружение падает сразу, не ждёт
// StartupTimeout.
func TestAssignFailsFastWhenAgentSessionFailed(t *testing.T) {
	h := newHarness(t)
	h.cfg = agentHost(h, func(c usecase.HostConfig) {
		url := "https://" + c.PrivateIP + ":8443"
		h.agent.Register(url, "token-1", c.Name, "python")
		h.agent.SetState(url, "FAILED")
	})
	sid := h.request()
	if _, err := runOnce(t, h.worker("w1")); err != nil {
		t.Fatalf("RunOnce err = %v", err)
	}
	env := h.env(sid)
	if env.State != domain.StateFailed || !strings.Contains(env.LastError, "agent state FAILED") {
		t.Fatalf("env = %+v", env)
	}
	if n := h.agent.Calls("Assign"); n != 1 {
		t.Fatalf("assign calls = %d, want 1", n)
	}
	if n := h.resources(); n != 0 {
		t.Fatalf("resources left = %d", n)
	}
}

// Агент VPS уже назначен другой сессии: /assign отвечает 409, ждать StartupTimeout бесполезно.
func TestAssignRejectedCleansUpWithoutWaiting(t *testing.T) {
	h := newHarness(t)
	h.cfg = agentHost(h, func(c usecase.HostConfig) {
		h.agent.Register("https://"+c.PrivateIP+":8443", "other-token", uuid.New(), "python")
	})
	sid := h.request()
	if _, err := runOnce(t, h.worker("w1")); err != nil {
		t.Fatal(err)
	}
	env := h.env(sid)
	if env.State != domain.StateFailed || !strings.Contains(env.LastError, "rejected") {
		t.Fatalf("env = %+v", env)
	}
	if n := h.agent.Calls("Assign"); n != 1 {
		t.Fatalf("assign calls = %d", n)
	}
	if n := h.resources(); n != 0 {
		t.Fatalf("resources left = %d", n)
	}
}

func (h *harness) requestDelete(sid uuid.UUID) {
	h.t.Helper()
	if err := h.svc.RequestDelete(context.Background(), sid, "test"); err != nil {
		h.t.Fatal(err)
	}
}

func TestDeleteDrainsAndStopsAgent(t *testing.T) {
	h := newHarness(t)
	sid := h.ready()
	h.requestDelete(sid)
	if ok, err := runOnce(t, h.worker("w1")); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	if env := h.env(sid); env.State != domain.StateDeleted || env.Endpoint != "" {
		t.Fatalf("env = %+v", env)
	}
	if h.agent.Calls("Drain") != 1 || h.agent.Calls("Stop") != 1 || h.resources() != 0 {
		t.Fatalf("drain=%d stop=%d resources=%d", h.agent.Calls("Drain"), h.agent.Calls("Stop"), h.resources())
	}
}

func TestDeleteAgentUnavailableStillDeletes(t *testing.T) {
	h := newHarness(t)
	sid := h.ready()
	h.agent.Fail("Drain", errors.New("connection refused"))
	h.agent.Fail("Stop", errors.New("connection refused"))
	h.requestDelete(sid)
	if ok, err := runOnce(t, h.worker("w1")); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	if env := h.env(sid); env.State != domain.StateDeleted {
		t.Fatalf("state = %s", env.State)
	}
	if h.agent.Calls("Drain") != 1 || h.resources() != 0 {
		t.Fatalf("drain calls = %d, resources = %d", h.agent.Calls("Drain"), h.resources())
	}
}

func TestDelete404Success(t *testing.T) {
	h := newHarness(t)
	sid := h.ready()
	env := h.env(sid)
	// Сервер и том уже исчезли вне воркера: удаление отсутствующего — успех.
	if err := h.cloud.DeleteServer(context.Background(), env.Resources.ServerID); err != nil {
		t.Fatal(err)
	}
	if err := h.cloud.DeleteVolume(context.Background(), env.Resources.VolumeID); err != nil {
		t.Fatal(err)
	}
	h.requestDelete(sid)
	if ok, err := runOnce(t, h.worker("w1")); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	if env := h.env(sid); env.State != domain.StateDeleted || env.Resources != (domain.Resources{}) {
		t.Fatalf("env = %+v", env)
	}
	if h.resources() != 0 {
		t.Fatal("port must be deleted")
	}
}

func TestDeleteFindsUnsavedResourcesByMetadata(t *testing.T) {
	h := newHarness(t)
	sid := h.ready()
	extra := h.cloud.Add("volume", sid) // ресурс сессии, ID которого не попал в БД
	h.requestDelete(sid)
	if ok, err := runOnce(t, h.worker("w1")); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	if h.env(sid).State != domain.StateDeleted || h.resources() != 0 {
		t.Fatalf("state = %s, resources = %d (extra %s)", h.env(sid).State, h.resources(), extra)
	}
}

func TestDeleteTransientRetriesWithBackoff(t *testing.T) {
	h := newHarness(t)
	sid := h.ready()
	h.requestDelete(sid)
	w := h.worker("w1")
	want := []time.Duration{20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second,
		320 * time.Second, 10 * time.Minute, 10 * time.Minute}
	for i, d := range want {
		h.cloud.Fail("DeleteServer", fmt.Errorf("503 from nova: %w", usecase.ErrCloudTransient))
		before := h.clock.Now()
		if ok, err := runOnce(t, w); !ok || err != nil {
			t.Fatalf("run %d: RunOnce = %v %v", i, ok, err)
		}
		j, _ := h.store.job(sid)
		if j.status != "queued" || j.nextRun.Sub(before) != d {
			t.Fatalf("run %d: job status %s, next_run_at = +%v, want +%v", i, j.status, j.nextRun.Sub(before), d)
		}
		if s := h.env(sid).State; s != domain.StateDeleting {
			t.Fatalf("run %d: state = %s", i, s)
		}
		if ok, _ := runOnce(t, w); ok {
			t.Fatalf("run %d: job claimed before next_run_at", i)
		}
		h.clock.Advance(d)
	}
	if ok, err := runOnce(t, w); !ok || err != nil {
		t.Fatalf("final RunOnce = %v %v", ok, err)
	}
	if h.env(sid).State != domain.StateDeleted || h.resources() != 0 {
		t.Fatalf("state = %s, resources = %d", h.env(sid).State, h.resources())
	}
}

func TestDeletePermanentErrorStopsRetrying(t *testing.T) {
	h := newHarness(t)
	sid := h.ready()
	h.requestDelete(sid)
	h.cloud.Fail("DeleteServer", fmt.Errorf("403 forbidden: %w", usecase.ErrCloudPermanent))
	w := h.worker("w1")
	if ok, err := runOnce(t, w); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	env := h.env(sid)
	if env.State != domain.StateCleanupPending || !strings.Contains(env.CleanupResult, "403") {
		t.Fatalf("env = %+v", env)
	}
	if j, _ := h.store.job(sid); j.status != "done" {
		t.Fatalf("job = %+v", j)
	}
	h.clock.Advance(24 * time.Hour)
	if ok, err := runOnce(t, w); ok || err != nil {
		t.Fatalf("no retry expected, got %v %v", ok, err)
	}
	if h.cloud.Calls("DeleteServer") != 1 {
		t.Fatalf("DeleteServer calls = %d", h.cloud.Calls("DeleteServer"))
	}
	// Повторный RequestDelete запускает удаление снова.
	h.requestDelete(sid)
	if ok, err := runOnce(t, w); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	if env := h.env(sid); env.State != domain.StateDeleted || h.resources() != 0 {
		t.Fatalf("env = %+v", env)
	}
}

func TestProvisioningFailureCleansUpToFailed(t *testing.T) {
	h := newHarness(t)
	sid := h.request()
	// Порт и том уже созданы, сервер — постоянная ошибка: очистка в том же задании, без RequestDelete.
	h.cloud.Fail("EnsureServer", fmt.Errorf("quota exceeded: %w", usecase.ErrCloudPermanent))
	if ok, err := runOnce(t, h.worker("w1")); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	env := h.env(sid)
	if env.State != domain.StateFailed || !strings.Contains(env.LastError, "quota") || h.resources() != 0 {
		t.Fatalf("env = %+v, resources = %d", env, h.resources())
	}
	if hist := fmt.Sprint(h.store.history(sid)); !strings.HasSuffix(hist, "CREATING CLEANUP_PENDING FAILED]") {
		t.Fatalf("history = %s", hist)
	}
	if j, _ := h.store.job(sid); j.status != "done" || j.job.Kind != usecase.JobCreate {
		t.Fatalf("job = %+v", j)
	}
}

func TestReadyCreateJobAfterTimeoutCompletes(t *testing.T) {
	h := newHarness(t)
	sid := h.request()
	h.store.failCompleteOnce(errors.New("db is down"))
	w := h.worker("w1")
	if ok, err := runOnce(t, w); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	if j, _ := h.store.job(sid); j.status != "queued" || h.env(sid).State != domain.StateReady {
		t.Fatalf("setup: job = %+v, state = %s", j, h.env(sid).State)
	}
	// Задание READY-окружения взято снова уже после StartupTimeout: только Complete, без очистки.
	h.clock.Advance(2 * h.s.StartupTimeout)
	if ok, err := runOnce(t, w); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	if j, _ := h.store.job(sid); j.status != "done" {
		t.Fatalf("job = %+v", j)
	}
	if env := h.env(sid); env.State != domain.StateReady || h.resources() != 3 {
		t.Fatalf("env = %+v, resources = %d", env, h.resources())
	}
}

func TestReconcileRetriesCleanupPending(t *testing.T) {
	h := newHarness(t)
	w := h.worker("w1")
	// Ошибка создания, очистка которой упала постоянной ошибкой.
	failed := h.request()
	h.cloud.Fail("EnsureServer", fmt.Errorf("quota exceeded: %w", usecase.ErrCloudPermanent))
	h.cloud.Fail("DeleteVolume", fmt.Errorf("403: %w", usecase.ErrCloudPermanent))
	if ok, err := runOnce(t, w); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	// Удаление пользователем, тоже упавшее постоянной ошибкой.
	deleted := h.ready()
	h.requestDelete(deleted)
	h.cloud.Fail("DeleteServer", fmt.Errorf("403: %w", usecase.ErrCloudPermanent))
	if ok, err := runOnce(t, w); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	for _, sid := range []uuid.UUID{failed, deleted} {
		if env := h.env(sid); env.State != domain.StateCleanupPending || env.CleanupResult == "" {
			t.Fatalf("setup: env = %+v", env)
		}
	}
	if ok, _ := runOnce(t, w); ok {
		t.Fatal("no job expected before reconcile")
	}

	if err := usecase.Reconcile(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if ok, err := runOnce(t, w); !ok || err != nil {
			t.Fatalf("RunOnce %d = %v %v", i, ok, err)
		}
	}
	if env := h.env(failed); env.State != domain.StateFailed || env.LastError == "" {
		t.Fatalf("provisioning failure must end FAILED: %+v", env)
	}
	if env := h.env(deleted); env.State != domain.StateDeleted {
		t.Fatalf("user delete must end DELETED: %+v", env)
	}
	if h.resources() != 0 {
		t.Fatalf("resources left = %d", h.resources())
	}
}

func TestRenewFailuresForLeaseStopStep(t *testing.T) {
	h := newHarness(t)
	h.host.Block = make(chan struct{})
	defer close(h.host.Block)
	sid := h.request()
	done := make(chan error, 1)
	go func() { _, err := runOnce(t, h.worker("w1")); done <- err }()
	waitFor(t, "Configure", func() bool { return h.host.Calls() == 1 })

	h.store.setRenewErr(errors.New("db is down"))
	start := h.store.renews()
	waitFor(t, "failed renewals", func() bool { return h.store.renews() >= start+3 })
	select {
	case err := <-done:
		t.Fatalf("step stopped before Lease passed without renewal: %v", err)
	default:
	}
	h.clock.Advance(testLease)
	select {
	case err := <-done:
		if !errors.Is(err, usecase.ErrLeaseLost) {
			t.Fatalf("err = %v, want ErrLeaseLost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("step was not cancelled after Lease without successful renewal")
	}
	if env := h.env(sid); env.State != domain.StateConfiguring {
		t.Fatalf("state = %s", env.State)
	}
}

func TestCreateTransientErrorRetries(t *testing.T) {
	h := newHarness(t)
	sid := h.request()
	h.cloud.Fail("EnsurePort", fmt.Errorf("timeout: %w", usecase.ErrCloudTransient))
	w := h.worker("w1")
	before := h.clock.Now()
	if ok, err := runOnce(t, w); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	if j, _ := h.store.job(sid); j.status != "queued" || j.nextRun.Sub(before) != 20*time.Second {
		t.Fatalf("job = %+v", j)
	}
	h.clock.Advance(20 * time.Second)
	if ok, err := runOnce(t, w); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	if h.env(sid).State != domain.StateReady {
		t.Fatalf("state = %s", h.env(sid).State)
	}
}

func TestReconcileDeletesOrphans(t *testing.T) {
	h := newHarness(t)
	active := h.ready()
	finished := h.ready()
	h.requestDelete(finished)
	if ok, err := runOnce(t, h.worker("w1")); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	leftover := h.cloud.Add("volume", finished)
	orphan := h.cloud.Add("server", uuid.New())
	orphanPort := h.cloud.Add("port", uuid.New())
	unlabeled := h.cloud.Add("port", uuid.Nil) // без session_id не трогаем
	// Ресурсы с cellestial_env другого окружения адаптер не возвращает из ListOwned, поэтому в fake
	// они не попадают и воркер их не видит.

	if err := usecase.Reconcile(context.Background(), h.worker("w1")); err != nil {
		t.Fatal(err)
	}
	owned, _ := h.cloud.ListOwned(context.Background())
	ids := map[string]bool{}
	for _, r := range owned {
		ids[r.ID] = true
		if r.SessionID != active && r.SessionID != uuid.Nil {
			t.Fatalf("resource %+v survived reconcile", r)
		}
	}
	if ids[leftover] || ids[orphan] || ids[orphanPort] || !ids[unlabeled] || len(owned) != 4 {
		t.Fatalf("owned after reconcile = %+v", owned)
	}
	if h.env(active).State != domain.StateReady {
		t.Fatal("active environment changed")
	}
}

// countingHost считает одновременные вызовы Configure.
type countingHost struct {
	inner    usecase.HostConfigurator
	mu       sync.Mutex
	cur, max int
}

func (c *countingHost) Configure(ctx context.Context, cfg usecase.HostConfig) error {
	c.mu.Lock()
	c.cur++
	c.max = max(c.max, c.cur)
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.cur--
		c.mu.Unlock()
	}()
	return c.inner.Configure(ctx, cfg)
}

func (c *countingHost) stats() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur, c.max
}

func TestConcurrencyLimit(t *testing.T) {
	h := newHarness(t)
	block := make(chan struct{})
	h.host.Block = block
	ch := &countingHost{inner: h.host}
	h.cfg = ch
	sids := make([]uuid.UUID, 0, 4)
	for range 4 {
		sids = append(sids, h.request())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.worker("w1").Run(ctx) }()

	waitFor(t, "two configures", func() bool { cur, _ := ch.stats(); return cur == 2 })
	time.Sleep(50 * time.Millisecond)
	if cur, mx := ch.stats(); cur != 2 || mx != 2 || h.host.Calls() != 2 {
		t.Fatalf("in Configure: cur=%d max=%d calls=%d", cur, mx, h.host.Calls())
	}
	close(block)
	waitFor(t, "all READY", func() bool {
		for _, sid := range sids {
			if h.env(sid).State != domain.StateReady {
				return false
			}
		}
		return true
	})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after ctx cancel")
	}
	if _, mx := ch.stats(); mx != 2 {
		t.Fatalf("max concurrent Configure = %d", mx)
	}
}

func TestCreateSavesIDReturnedWithError(t *testing.T) {
	h := newHarness(t)
	sid := h.request()
	// Сервер создан, но ожидание ACTIVE упало: ID обязан попасть в БД до обработки ошибки.
	h.cloud.FailAfterOnce("EnsureServer", fmt.Errorf("wait active: %w", usecase.ErrCloudTransient))
	w := h.worker("w1")
	if ok, err := runOnce(t, w); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	if env := h.env(sid); env.Resources.ServerID == "" || env.State != domain.StateCreating {
		t.Fatalf("env = %+v", env)
	}
	h.clock.Advance(time.Minute)
	if ok, err := runOnce(t, w); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	if h.env(sid).State != domain.StateReady || h.cloud.Count("server") != 1 {
		t.Fatalf("state = %s, servers = %d", h.env(sid).State, h.cloud.Count("server"))
	}
}

// assertFailedClean проверяет, что создание закончилось очисткой: FAILED, ресурсов нет, задание done.
func assertFailedClean(t *testing.T, h *harness, sid uuid.UUID, wantErr string) {
	t.Helper()
	env := h.env(sid)
	if env.State != domain.StateFailed || !strings.Contains(env.LastError, wantErr) || env.Endpoint != "" {
		t.Fatalf("env = %+v", env)
	}
	if hist := fmt.Sprint(h.store.history(sid)); !strings.Contains(hist, "CLEANUP_PENDING FAILED]") {
		t.Fatalf("history = %s", hist)
	}
	if n := h.resources(); n != 0 {
		t.Fatalf("resources left = %d", n)
	}
	if j, _ := h.store.job(sid); j.status != "done" {
		t.Fatalf("job = %+v", j)
	}
}

func TestCreateStuckServerTimesOut(t *testing.T) {
	h := newHarness(t)
	h.s.StartupTimeout = 200 * time.Millisecond
	h.cloud.hang = map[string]bool{"EnsureServer": true}
	sid := h.request()
	if ok, err := runOnce(t, h.worker("w1")); !ok || err != nil {
		t.Fatalf("RunOnce = %v %v", ok, err)
	}
	assertFailedClean(t, h, sid, "startup timeout exceeded")
}

func TestAssignHangDoesNotBlockTimeout(t *testing.T) {
	h := newHarness(t)
	ha := &hangingAssign{Agent: h.agent}
	h.ac = ha
	sid := h.request()
	w := h.worker("w1")
	usecase.SetAssignTimeout(w, 5*time.Millisecond)
	done := make(chan error, 1)
	go func() { _, err := runOnce(t, w); done <- err }()
	waitFor(t, "assign calls", func() bool { return ha.calls.Load() >= 2 })
	h.clock.Advance(h.s.StartupTimeout)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunOnce err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hung assign call blocked the startup timeout")
	}
	assertFailedClean(t, h, sid, "startup timeout exceeded")
}

func TestReconcilePassTimeout(t *testing.T) {
	h := newHarness(t)
	h.s.ReconcileInterval = 50 * time.Millisecond
	h.cloud.hang = map[string]bool{"ListOwned": true}
	done := make(chan error, 1)
	go func() { done <- usecase.ReconcilePass(context.Background(), h.worker("w1")) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want deadline exceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reconcile pass is not bounded by a timeout")
	}
}
