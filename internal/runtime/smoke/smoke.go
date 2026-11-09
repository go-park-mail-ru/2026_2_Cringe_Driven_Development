// Package smoke содержит ручную сквозную проверку модуля runtime: создание окружений,
// выполнение кода в агенте, удаление и поиск оставшихся облачных ресурсов.
package smoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"github.com/google/uuid"
)

const (
	// KillAfterVolume и KillAfterServer — шаги, после которых отменяется воркер.
	KillAfterVolume = "volume"
	KillAfterServer = "server"

	executionID     = "smoke-1"
	defaultLease    = time.Minute
	killLease       = 10 * time.Second
	defaultTimeout  = 30 * time.Minute
	idlePause       = 200 * time.Millisecond
	watchPause      = 20 * time.Millisecond
	executionPoll   = 500 * time.Millisecond
	smokeProfile    = "default"
	stateSucceeded  = "SUCCEEDED"
	stateFailedExec = "FAILED"
	stepTimeout     = 2 * time.Minute
)

// Executor выполняет код в агенте сессии (его реализуют agent.Client и fake.Agent).
type Executor interface {
	Execute(ctx context.Context, ep usecase.AgentEndpoint, executionID, code string) error
	Execution(ctx context.Context, ep usecase.AgentEndpoint, id string) (string, error)
	Events(ctx context.Context, ep usecase.AgentEndpoint, after int) ([]json.RawMessage, error)
}

// Lister перечисляет ресурсы облака с меткой рантайма.
type Lister interface {
	ListOwned(ctx context.Context) ([]usecase.CloudResource, error)
}

// Deps — зависимости smoke.
type Deps struct {
	Service usecase.EnvironmentService
	Repo    usecase.EnvironmentRepository
	Secrets usecase.Secrets
	Agent   Executor
	Cloud   Lister
	// NewWorker создаёт воркер с указанными владельцем и lease.
	NewWorker func(owner string, lease time.Duration) *usecase.Worker
}

// Options — параметры запуска.
type Options struct {
	Language string // python | r
	Parallel int    // число одновременных сессий, минимум 1
	// KillWorkerAfter: "", "volume" или "server" — отмена контекста воркера сразу после сохранения
	// VolumeID / ServerID; создание продолжает другой воркер (другой Owner) после истечения lease.
	KillWorkerAfter string
	Timeout         time.Duration
	// Lease — lease заданий воркеров smoke. По умолчанию 60 с, при KillWorkerAfter — 10 с
	// (воркер продлевает lease сам, поэтому короткий lease безопасен, а ожидание после «смерти»
	// первого воркера не превышает его).
	Lease time.Duration
}

// SessionReport — итог одной сессии.
type SessionReport struct {
	SessionID    string  `json:"session_id"`
	FinalState   string  `json:"final_state"`
	Ready        bool    `json:"ready"`
	Print42      bool    `json:"print42"`
	AgentSession bool    `json:"agent_session_matches"`
	WorkerKilled bool    `json:"worker_killed"`
	Seconds      float64 `json:"seconds"`
	// ReadySeconds — от запроса окружения до READY (с VPS пула — без создания VPS).
	ReadySeconds float64 `json:"ready_seconds"`
	Error        string  `json:"error,omitempty"`
}

// Report печатается как JSON.
type Report struct {
	Language       string          `json:"language"`
	Parallel       int             `json:"parallel"`
	KillWorker     string          `json:"kill_worker_after,omitempty"`
	Sessions       []SessionReport `json:"sessions"`
	TokensDistinct bool            `json:"tokens_distinct"`
	Leftovers      int             `json:"leftovers"`
	OK             bool            `json:"ok"`
	Error          string          `json:"error,omitempty"`
}

type sessionRun struct {
	deps  Deps
	opts  Options
	id    uuid.UUID
	token string
	rep   SessionReport
}

// Run выполняет сценарий; ошибка возвращается и когда отчёт построен, но проверка не пройдена.
func Run(ctx context.Context, deps Deps, opts Options) (Report, error) {
	if opts.Parallel < 1 {
		opts.Parallel = 1
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.Lease <= 0 {
		opts.Lease = defaultLease
		if opts.KillWorkerAfter != "" {
			opts.Lease = killLease
		}
	}
	rep := Report{Language: opts.Language, Parallel: opts.Parallel, KillWorker: opts.KillWorkerAfter}
	if !domain.ValidLanguage(opts.Language) {
		return fail(rep, fmt.Errorf("smoke: invalid language %q", opts.Language))
	}
	if k := opts.KillWorkerAfter; k != "" && k != KillAfterVolume && k != KillAfterServer {
		return fail(rep, fmt.Errorf("smoke: invalid kill-worker-after %q", k))
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	runs := make([]*sessionRun, opts.Parallel)
	var wg sync.WaitGroup
	for i := range runs {
		runs[i] = &sessionRun{deps: deps, opts: opts, id: uuid.New()}
		wg.Add(1)
		go func() {
			defer wg.Done()
			runs[i].run(ctx)
		}()
	}
	wg.Wait()

	ok := true
	tokens := map[string]bool{}
	ids := map[uuid.UUID]bool{}
	for _, r := range runs {
		rep.Sessions = append(rep.Sessions, r.rep)
		ids[r.id] = true
		if r.token != "" {
			tokens[r.token] = true
		}
		ok = ok && r.rep.Error == "" && r.rep.FinalState == string(domain.StateDeleted) && r.rep.Print42 && r.rep.AgentSession
	}
	rep.TokensDistinct = len(tokens) == opts.Parallel
	// Токены запрашиваются у сессий, дошедших до READY; при ошибках раньше проверка не применима.
	ok = ok && rep.TokensDistinct

	listCtx, listCancel := context.WithTimeout(context.WithoutCancel(ctx), stepTimeout)
	left, err := deps.Cloud.ListOwned(listCtx)
	listCancel()
	if err != nil {
		rep.Error = "list owned: " + err.Error()
		ok = false
	}
	for _, res := range left {
		if ids[res.SessionID] {
			rep.Leftovers++
		}
	}
	ok = ok && rep.Leftovers == 0
	rep.OK = ok
	if !ok {
		if rep.Error == "" {
			rep.Error = "smoke checks failed"
		}
		return rep, errors.New(rep.Error)
	}
	return rep, nil
}

func fail(rep Report, err error) (Report, error) {
	rep.Error = err.Error()
	return rep, err
}

func (s *sessionRun) run(ctx context.Context) {
	start := time.Now()
	s.rep.SessionID = s.id.String()
	err := s.safeScenario(ctx)
	if err != nil {
		s.rep.Error = err.Error()
	}
	// Состояние читаем независимо от отмены ctx — для отчёта.
	if env, gerr := s.deps.Repo.Get(context.WithoutCancel(ctx), s.id); gerr == nil {
		s.rep.FinalState = string(env.State)
	}
	if err != nil {
		if cerr := s.cleanup(ctx); cerr != nil {
			s.rep.Error += "; cleanup: " + cerr.Error()
		}
	}
	s.rep.Seconds = time.Since(start).Seconds()
}

// safeScenario превращает панику сценария в ошибку, чтобы выполнилась очистка.
func (s *sessionRun) safeScenario(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return s.scenario(ctx)
}

// cleanup по ошибке сценария удаляет окружение, чтобы не оставлять ресурсы. FAILED — конечное
// состояние, которого воркер достигает только после подтверждённой очистки.
func (s *sessionRun) cleanup(ctx context.Context) error {
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.opts.Timeout)
	defer cancel()
	if _, err := s.deps.Repo.Get(bg, s.id); err != nil {
		if errors.Is(err, usecase.ErrNotFound) {
			return nil // окружение не создавалось
		}
		return fmt.Errorf("get environment: %w", err)
	}
	if err := s.deps.Service.RequestDelete(bg, s.id, "smoke cleanup"); err != nil {
		return fmt.Errorf("request delete: %w", err)
	}
	err := s.drive(bg, s.deps.NewWorker(s.owner("cleanup"), s.opts.Lease), domain.StateDeleted)
	if env, gerr := s.deps.Repo.Get(bg, s.id); gerr == nil {
		s.rep.FinalState = string(env.State)
	}
	if err != nil {
		return fmt.Errorf("drive delete: %w", err)
	}
	return nil
}

func (s *sessionRun) owner(role string) string { return "smoke-" + s.id.String()[:8] + "-" + role }

func (s *sessionRun) scenario(ctx context.Context) error {
	start := time.Now()
	if _, err := s.deps.Service.RequestCreate(ctx, usecase.CreateEnvironment{
		SessionID: s.id, Language: s.opts.Language, Profile: smokeProfile,
	}); err != nil {
		return fmt.Errorf("request create: %w", err)
	}
	if err := s.create(ctx); err != nil {
		return err
	}
	s.rep.Ready = true
	s.rep.ReadySeconds = time.Since(start).Seconds()
	if err := s.check(ctx); err != nil {
		return err
	}
	if err := s.deps.Service.RequestDelete(ctx, s.id, "smoke"); err != nil {
		return fmt.Errorf("request delete: %w", err)
	}
	if err := s.drive(ctx, s.deps.NewWorker(s.owner("delete"), s.opts.Lease), domain.StateDeleted); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}

// create доводит окружение до READY; при KillWorkerAfter первый воркер «умирает» посреди создания.
func (s *sessionRun) create(ctx context.Context) error {
	first := s.deps.NewWorker(s.owner("a"), s.opts.Lease)
	if s.opts.KillWorkerAfter == "" {
		return s.drive(ctx, first, domain.StateReady)
	}
	killCtx, kill := context.WithCancel(ctx)
	defer kill()
	killed := make(chan struct{})
	go func() {
		defer close(killed)
		s.watch(killCtx, kill)
	}()
	err := s.drive(killCtx, first, domain.StateReady)
	kill()
	<-killed
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if env, gerr := s.deps.Repo.Get(ctx, s.id); gerr == nil && env.State == domain.StateReady && !s.rep.WorkerKilled {
		return errors.New("worker finished creation before kill point was reached")
	}
	if !s.rep.WorkerKilled {
		return fmt.Errorf("kill point %q not reached: %w", s.opts.KillWorkerAfter, err)
	}
	// Другой Owner; задание освободится после истечения lease первого воркера.
	return s.drive(ctx, s.deps.NewWorker(s.owner("b"), s.opts.Lease), domain.StateReady)
}

// watch отменяет контекст воркера, как только в окружении сохранён нужный ресурс.
func (s *sessionRun) watch(ctx context.Context, kill context.CancelFunc) {
	t := time.NewTicker(watchPause)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		env, err := s.deps.Repo.Get(ctx, s.id)
		if err != nil {
			continue
		}
		hit := (s.opts.KillWorkerAfter == KillAfterVolume && env.Resources.VolumeID != "") ||
			(s.opts.KillWorkerAfter == KillAfterServer && env.Resources.ServerID != "")
		if hit {
			s.rep.WorkerKilled = true
			kill()
			return
		}
	}
}

// drive выполняет задания воркера, пока окружение не достигнет want.
func (s *sessionRun) drive(ctx context.Context, w *usecase.Worker, want domain.State) error {
	var lastErr error
	for {
		env, err := s.deps.Repo.Get(ctx, s.id)
		if err != nil {
			return fmt.Errorf("get environment: %w", err)
		}
		switch {
		case env.State == want:
			return nil
		case env.State == domain.StateFailed && want == domain.StateDeleted:
			return nil // FAILED достигается только после подтверждённой очистки
		case env.State == domain.StateFailed || env.State == domain.StateCleanupPending && want != domain.StateDeleted:
			return fmt.Errorf("environment %s: %s", env.State, env.LastError)
		case env.State == domain.StateDeleted:
			return fmt.Errorf("environment unexpectedly %s", env.State)
		}
		ran, rerr := w.RunOnce(ctx)
		if rerr != nil {
			lastErr = rerr
		}
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return fmt.Errorf("%w (last: %w)", err, lastErr)
			}
			return err
		}
		if !ran {
			select {
			case <-ctx.Done():
			case <-time.After(idlePause):
			}
		}
	}
}

// check проверяет токен, агента и выполнение print(42).
func (s *sessionRun) check(ctx context.Context) error {
	env, err := s.deps.Repo.Get(ctx, s.id)
	if err != nil {
		return fmt.Errorf("get environment: %w", err)
	}
	sealed, err := s.deps.Repo.LoadSecrets(ctx, s.id)
	if err != nil {
		return fmt.Errorf("load secrets: %w", err)
	}
	tok, err := s.deps.Secrets.Open(sealed.Token)
	if err != nil {
		return fmt.Errorf("open token: %w", err)
	}
	s.token = string(tok)
	ep := usecase.AgentEndpoint{BaseURL: env.Endpoint, Token: s.token}
	if err := s.deps.Agent.Execute(ctx, ep, executionID, "print(42)"); err != nil {
		return fmt.Errorf("execute: %w", err)
	}
	if err := s.waitExecution(ctx, ep); err != nil {
		return err
	}
	return s.checkEvents(ctx, ep)
}

func (s *sessionRun) waitExecution(ctx context.Context, ep usecase.AgentEndpoint) error {
	ctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	for {
		state, err := s.deps.Agent.Execution(ctx, ep, executionID)
		if err != nil {
			return fmt.Errorf("execution: %w", err)
		}
		switch state {
		case stateSucceeded:
			return nil
		case stateFailedExec:
			return errors.New("execution FAILED")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(executionPoll):
		}
	}
}

// wantStdout — вывод print(42): в R печатается с индексом вектора.
func wantStdout(language string) string {
	if language == "r" {
		return "[1] 42\n"
	}
	return "42\n"
}

func (s *sessionRun) checkEvents(ctx context.Context, ep usecase.AgentEndpoint) error {
	ctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	want := wantStdout(s.opts.Language)
	for {
		events, err := s.deps.Agent.Events(ctx, ep, 0)
		if err != nil {
			return fmt.Errorf("events: %w", err)
		}
		for _, raw := range events {
			var ev struct {
				SessionID string `json:"session_id"`
				MsgType   string `json:"msg_type"`
				Content   struct {
					Name string `json:"name"`
					Text string `json:"text"`
				} `json:"content"`
			}
			if json.Unmarshal(raw, &ev) != nil {
				continue
			}
			if ev.MsgType == "stream" && ev.Content.Name == "stdout" && ev.Content.Text == want {
				s.rep.Print42 = true
				s.rep.AgentSession = ev.SessionID == s.id.String()
				if !s.rep.AgentSession {
					return fmt.Errorf("agent answered with session_id %q", ev.SessionID)
				}
				return nil
			}
		}
		// Событие могло ещё не попасть в журнал.
		select {
		case <-ctx.Done():
			return fmt.Errorf("stdout %q not found in events: %w", want, ctx.Err())
		case <-time.After(executionPoll):
		}
	}
}
