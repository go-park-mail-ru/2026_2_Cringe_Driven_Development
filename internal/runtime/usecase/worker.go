package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
)

// WorkerSettings — настройки воркера.
type WorkerSettings struct {
	Owner                                                       string
	Concurrency                                                 int
	Lease, PollInterval, ReconcileInterval                      time.Duration
	StartupTimeout, AnsibleTimeout, DrainTimeout, DeleteTimeout time.Duration
	PythonImage, KernelDNS                                      string
	BlockedPublicIPs                                            []string
	EnvName                                                     string
	// PoolSize — RUNTIME_WARM_POOL_SIZE; 0 выключает пул целиком: нет захвата, подготовки и пополнения.
	PoolSize int
	// ImageID и AgentVersion — текущие образ VPS и версия агента; VPS пула с другими удаляются.
	ImageID, AgentVersion string
}

const (
	agentPort             = "8443"
	httpStatusOK          = 200
	httpStatusBadRequest  = 400
	httpStatusConflict    = 409
	backoffBase           = 10 * time.Second
	backoffMax            = 10 * time.Minute
	maxStaleRetries       = 5
	defaultVerifyInterval = 5 * time.Second
	defaultHealthTimeout  = 10 * time.Second
	defaultAssignTimeout  = time.Minute
	maxReconcilePass      = 2 * time.Minute
)

var (
	// errDeleteRequested — причина отмены шага создания: задание стало delete.
	errDeleteRequested = errors.New("delete requested")
	// errStartupTimeout — окружение не стало READY за StartupTimeout от CreatedAt.
	errStartupTimeout = errors.New("startup timeout exceeded")
	// errAssignRejected — агент отклонил назначение (400/409): повтор бесполезен.
	errAssignRejected = errors.New("agent rejected the assignment")
)

// Worker выполняет задания создания и удаления окружений.
type Worker struct {
	repo           EnvironmentRepository
	pool           PoolRepository
	q              JobQueue
	cloud          CloudProvider
	host           HostConfigurator
	agent          AgentClient
	sec            Secrets
	clock          Clock
	s              WorkerSettings
	log            *slog.Logger
	verifyInterval time.Duration
	healthTimeout  time.Duration // лимит одного вызова /health при проверке
	assignTimeout  time.Duration // лимит одного вызова /assign
	poolKick       chan struct{}
	poolMu         sync.Mutex
	poolFailures   int       // подряд неудачных подготовок VPS пула
	poolRetryAt    time.Time // до этого момента пул не пополняется (backoff)
}

// NewWorker создаёт воркер; нулевые настройки заменяются умолчаниями. pool = nil допустим только при
// PoolSize = 0 (тесты и smoke без таблицы пула): тогда reconcile не трогает пул.
func NewWorker(repo EnvironmentRepository, pool PoolRepository, q JobQueue, cloud CloudProvider, host HostConfigurator,
	agent AgentClient, sec Secrets, clock Clock, s WorkerSettings,
) *Worker {
	s.Concurrency = max(s.Concurrency, 1)
	s.Lease = orDefault(s.Lease, time.Minute)
	s.PollInterval = orDefault(s.PollInterval, time.Second)
	s.ReconcileInterval = orDefault(s.ReconcileInterval, 5*time.Minute)
	s.StartupTimeout = orDefault(s.StartupTimeout, 20*time.Minute)
	s.AnsibleTimeout = orDefault(s.AnsibleTimeout, 15*time.Minute)
	s.DrainTimeout = orDefault(s.DrainTimeout, 30*time.Second)
	s.DeleteTimeout = orDefault(s.DeleteTimeout, 10*time.Minute)
	return &Worker{
		repo: repo, pool: pool, q: q, cloud: cloud, host: host, agent: agent, sec: sec, clock: clock, s: s,
		log:            slog.Default().With("component", "runtime-worker", "owner", s.Owner),
		verifyInterval: defaultVerifyInterval,
		healthTimeout:  defaultHealthTimeout,
		assignTimeout:  defaultAssignTimeout,
		poolKick:       make(chan struct{}, 1),
	}
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// Run обрабатывает задания (не более Concurrency одновременно) и периодически ищет сирот
// до отмены ctx; возвращает nil после остановки всех горутин.
func (w *Worker) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.reconcileLoop(ctx)
	}()
	if w.s.PoolSize > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.poolLoop(ctx)
		}()
	}
	sem := make(chan struct{}, w.s.Concurrency)
	poll := time.NewTicker(w.s.PollInterval)
	defer poll.Stop()
	for {
		w.claimAvailable(ctx, sem, &wg)
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
		}
	}
}

// claimAvailable берёт задания, пока есть свободные слоты и готовые задания.
func (w *Worker) claimAvailable(ctx context.Context, sem chan struct{}, wg *sync.WaitGroup) {
	for {
		select {
		case sem <- struct{}{}:
		default:
			return
		}
		job, ok, err := w.q.Claim(ctx, w.s.Owner, w.s.Lease)
		if err != nil || !ok {
			<-sem
			if err != nil && ctx.Err() == nil {
				w.log.Error("claim job", "error", err)
			}
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := w.process(ctx, job); err != nil && ctx.Err() == nil {
				w.log.Warn("job stopped", "job_id", job.ID, "session_id", job.SessionID, "error", err)
			}
		}()
	}
}

// RunOnce берёт и выполняет одно задание; false — готовых заданий нет.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	job, ok, err := w.q.Claim(ctx, w.s.Owner, w.s.Lease)
	if err != nil {
		return false, fmt.Errorf("claim job: %w", err)
	}
	if !ok {
		return false, nil
	}
	return true, w.process(ctx, job)
}

// process выполняет задание под lease: фоновое продление отменяет шаги при потере lease
// (leaseCtx) и шаги создания при переводе задания в delete (createCtx).
func (w *Worker) process(ctx context.Context, job Job) error {
	leaseCtx, cancelLease := context.WithCancelCause(ctx)
	defer cancelLease(nil)
	createCtx, cancelCreate := context.WithCancelCause(leaseCtx)
	defer cancelCreate(nil)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.keepLease(leaseCtx, stop, job, cancelLease, cancelCreate)
	}()
	defer func() {
		close(stop)
		wg.Wait()
	}()

	r := &jobRun{w: w, job: job, leaseCtx: leaseCtx, createCtx: createCtx}
	return r.execute()
}

// keepLease продлевает lease каждые Lease/3. ErrLeaseLost или отсутствие успешного продления
// дольше Lease (по Clock: за это время lease мог истечь и задание взяла другая реплика) отменяют
// leaseCtx; смена вида задания на delete отменяет createCtx.
func (w *Worker) keepLease(ctx context.Context, stop <-chan struct{}, job Job,
	cancelLease, cancelCreate context.CancelCauseFunc,
) {
	t := time.NewTicker(max(w.s.Lease/3, time.Millisecond))
	defer t.Stop()
	lastOK := w.clock.Now()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
		}
		kind, err := w.renew(ctx, job)
		switch {
		case errors.Is(err, ErrLeaseLost):
			cancelLease(ErrLeaseLost)
			return
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			if w.clock.Now().Sub(lastOK) >= w.s.Lease {
				w.log.Error("lease not renewed for a whole lease period", "job_id", job.ID, "error", err)
				cancelLease(fmt.Errorf("%w: renew failing: %w", ErrLeaseLost, err))
				return
			}
			w.log.Warn("renew lease", "job_id", job.ID, "error", err)
		default:
			lastOK = w.clock.Now()
			if kind == JobDelete && job.Kind == JobCreate {
				cancelCreate(errDeleteRequested)
			}
		}
	}
}

// renew продлевает lease; вызов ограничен Lease/3, чтобы зависший запрос к БД не помешал
// заметить, что lease не продлевался дольше Lease.
func (w *Worker) renew(ctx context.Context, job Job) (JobKind, error) {
	ctx, cancel := context.WithTimeout(ctx, max(w.s.Lease/3, time.Millisecond))
	defer cancel()
	return w.q.Renew(ctx, job, w.s.Owner, w.s.Lease)
}

// jobRun — состояние выполнения одного задания.
type jobRun struct {
	w         *Worker
	job       Job
	leaseCtx  context.Context // отменяется при потере lease и остановке воркера
	createCtx context.Context // дополнительно отменяется, когда задание стало delete
	drained   bool
}

// execute на каждой итерации перечитывает окружение и выполняет один шаг по его состоянию.
func (r *jobRun) execute() error {
	stale := 0
	for {
		if err := r.leaseErr(); err != nil {
			return err
		}
		env, err := r.w.repo.Get(r.leaseCtx, r.job.SessionID)
		if errors.Is(err, ErrNotFound) {
			return r.complete()
		}
		if err != nil {
			return fmt.Errorf("get environment: %w", err)
		}
		if env.DesiredState == domain.StateDeleted && r.job.Kind == JobCreate {
			if serr := r.syncKind(); serr != nil {
				return serr
			}
		}
		deleting := r.deleting(env)
		var done bool
		if deleting {
			done, err = r.deleteStep(env)
		} else {
			done, err = r.createStep(env)
		}
		if err == nil {
			if done {
				return nil
			}
			stale = 0
			continue
		}
		if lerr := r.leaseErr(); lerr != nil {
			return lerr
		}
		switch {
		case errors.Is(err, ErrLeaseLost):
			return err
		case errors.Is(err, ErrStale):
			if stale++; stale > maxStaleRetries {
				return err
			}
			if serr := r.syncKind(); serr != nil {
				return serr
			}
		case !deleting && errors.Is(context.Cause(r.createCtx), errDeleteRequested):
			r.job.Kind = JobDelete
		default:
			var stop bool
			if deleting {
				stop, err = r.deleteFailed(err)
			} else {
				stop, err = r.createFailed(err)
			}
			if stop {
				return err
			}
		}
	}
}

func (r *jobRun) deleting(env domain.Environment) bool {
	return r.job.Kind == JobDelete || env.DesiredState == domain.StateDeleted || env.State.Finished() ||
		env.State == domain.StateDeleting || env.State == domain.StateCleanupPending
}

func (r *jobRun) leaseErr() error {
	if r.leaseCtx.Err() != nil {
		return context.Cause(r.leaseCtx)
	}
	return nil
}

// syncKind продлевает lease и берёт текущий вид задания (MarkForDelete мог сменить его на delete).
func (r *jobRun) syncKind() error {
	kind, err := r.w.renew(r.leaseCtx, r.job)
	if err != nil {
		return err
	}
	r.job.Kind = kind
	return nil
}

// save сохраняет окружение в состоянии to; смена состояния допустима только по CanTransition.
func (r *jobRun) save(env domain.Environment, to domain.State) (domain.Environment, error) {
	if to != env.State && !domain.CanTransition(env.State, to) {
		return env, fmt.Errorf("invalid transition %s → %s", env.State, to)
	}
	env.State = to
	return r.w.repo.Save(r.leaseCtx, env, r.w.s.Owner)
}

// complete завершает задание. Если вид задания сменился (create → delete), возвращает ErrStale,
// чтобы цикл перечитал окружение и продолжил удалением.
func (r *jobRun) complete() error {
	err := r.w.q.Complete(r.leaseCtx, r.job, r.w.s.Owner)
	if !errors.Is(err, ErrLeaseLost) {
		return err
	}
	prev := r.job.Kind
	if err := r.syncKind(); err != nil {
		return err
	}
	if r.job.Kind != prev {
		return ErrStale
	}
	return ErrLeaseLost
}

func (r *jobRun) finish() (bool, error) {
	if err := r.complete(); err != nil {
		return false, err
	}
	return true, nil
}

// retry возвращает задание в очередь с экспоненциальной задержкой.
func (r *jobRun) retry(cause error) error {
	at := r.w.clock.Now().Add(backoff(r.job.Attempt))
	if err := r.w.q.Retry(r.leaseCtx, r.job, r.w.s.Owner, at); err != nil {
		return fmt.Errorf("retry job: %w", err)
	}
	r.w.log.Info("job retry scheduled", "job_id", r.job.ID, "session_id", r.job.SessionID,
		"kind", r.job.Kind, "attempt", r.job.Attempt, "next_run_at", at, "cause", cause)
	return nil
}

// backoff = min(2^attempt·10s, 10m).
func backoff(attempt int) time.Duration {
	d := backoffBase
	for i := 0; i < max(attempt, 1) && d < backoffMax; i++ {
		d *= 2
	}
	return min(d, backoffMax)
}
