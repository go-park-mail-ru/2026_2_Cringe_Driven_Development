package usecase

import (
	"context"
	"errors"
	"log/slog"
	"time"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
)

// Worker в фоне готовит сессии, доставляет команды, архивирует результаты и завершает окружения.
type Worker struct {
	// store Хранилище пользовательских сессий, команд и событий.
	store Store
	// env Сервис подготовки окружений.
	env Environments
	// agent Внутренний клиент агента.
	agent SessionAgent
	// output Адаптер записи результата в ячейку.
	output OutputWriter
	// now Подменяемые часы для воспроизводимого поведения.
	now Clock
	// id Идентификатор фонового обработчика или тестового объекта.
	id string
}

// NewWorker собирает фоновый обработчик сессий с отдельным ID владельца задач и зависимостями через порты.
func NewWorker(s Store, e Environments, a SessionAgent, out OutputWriter, now Clock) *Worker {
	if now == nil {
		now = time.Now
	}
	return &Worker{s, e, a, out, now, uuid.NewString()}
}

// Run периодически обрабатывает сохранённые сессии до отмены контекста.
func (w *Worker) Run(ctx context.Context) error {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			l, err := w.store.Claim(ctx, w.id)
			if err == nil {
				if err = w.Step(ctx, l); err != nil && !errors.Is(err, D.ErrLeaseLost) {
					slog.WarnContext(ctx, "runtime session step failed", "session_id", l.Session.ID, "error_code", "runtime_step_failed")
				}
			}
			if err != nil && !errors.Is(err, D.ErrNotFound) && !errors.Is(err, D.ErrLeaseLost) && ctx.Err() == nil {
				slog.WarnContext(ctx, "runtime session work deferred", "error_code", "runtime_store_failed")
			}
			if w.output != nil {
				if err = w.store.Project(ctx, w.output); err != nil && !errors.Is(err, D.ErrNotFound) && ctx.Err() == nil {
					slog.WarnContext(ctx, "runtime output projection deferred", "error_code", "runtime_projection_failed")
				}
			}
		}
	}
}

// Step выполняет один ограниченный по времени шаг жизненного цикла сессии и сохраняет его результат.
func (w *Worker) Step(parent context.Context, l D.Lease) error {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	v := l.Session
	v.NextCheck = w.now().Add(2 * time.Second)
	var err error
	switch v.State {
	case D.Provisioning, D.Starting:
		err = w.provision(ctx, l, &v)
	case D.Ready, D.Reconnecting:
		err = w.ready(ctx, l, &v)
	case D.Stopping:
		err = w.cleanup(ctx, l, &v)
	case D.Stopped, D.Failed:
		return nil
	default:
		return D.ErrProtocol
	}
	if err != nil {
		return err
	}
	return w.store.Save(ctx, l, v)
}

// reason пропускает только известные безопасные коды причин, скрывая внутренние ошибки сервисов.
func reason(code string) string {
	switch code {
	case "requested", "idle_timeout", "kernel_died", "agent_unreachable", "protocol_error", "environment_failed", "environment_lost", "capacity_exceeded", "startup_timeout":
		return code
	default:
		return "kernel_died"
	}
}

// finish выбирает конечное состояние сессии по причине завершения после обработки очистки.
func (w *Worker) finish(v *D.Session) {
	v.State = D.Failed
	v.Stage = "failed"
	if v.Reason == "requested" || v.Reason == "idle_timeout" {
		v.State = D.Stopped
		v.Stage = "stopped"
	}
	v.CleanupError = false
	v.CreateUnknown = false
}

// provision запрашивает окружение, отслеживает его подготовку и проверяет UUID и язык готового агента.
func (w *Worker) provision(ctx context.Context, l D.Lease, v *D.Session) error {
	if err := w.store.CheckLease(ctx, l); err != nil {
		return err
	}
	env, err := w.env.Get(ctx, v.ID)
	if errors.Is(err, D.ErrNotFound) {
		v.CreateSent = true
		v.CreateUnknown = true
		if err = w.store.Checkpoint(ctx, l, *v); err != nil {
			return err
		}
		if err = w.store.CheckLease(ctx, l); err != nil {
			return err
		}
		env, err = w.env.RequestCreate(ctx, D.EnvironmentRequest{SessionID: v.ID, Language: v.Language, Profile: "default"})
		switch {
		case errors.Is(err, D.ErrQueueFull):
			v.Reason = "capacity_exceeded"
			w.finish(v)
			return nil
		case errors.Is(err, D.ErrInvalid), errors.Is(err, D.ErrConflict):
			v.Reason = "environment_failed"
			w.finish(v)
			return nil
		case err != nil:
			return nil
		}
	} else if err != nil {
		return err
	}
	v.CreateUnknown = false
	switch env.State {
	case "QUEUED":
		v.State = D.Provisioning
		v.Stage = "queued"
	case "CREATING":
		v.State = D.Provisioning
		v.Stage = "creating_vps"
	case "CONFIGURING":
		v.State = D.Starting
		v.Stage = "configuring_agent"
	case "VERIFYING":
		v.State = D.Starting
		v.Stage = "verifying_agent"
	case "READY":
		if err = w.store.CheckLease(ctx, l); err != nil {
			return err
		}
		v.AgentStarted = true
		h, healthErr := w.agent.Health(ctx, v.ID)
		if healthErr != nil {
			return w.connectionFailure(v, healthErr)
		}
		if h.SessionID != v.ID || h.Language != v.Language {
			v.State = D.Stopping
			v.Stage = "stopping"
			v.Reason = "protocol_error"
			v.AgentStarted = true
			return nil
		}
		switch {
		case h.Ready:
			v.State = D.Ready
			v.Stage = "ready"
			v.AgentStarted = true
			v.Generation = h.Generation
			v.Retry = 0
		case h.State == "STOPPED" || h.State == "FAILED":
			v.State = D.Stopping
			v.Reason = reason(h.Reason)
			v.AgentStarted = true
		default:
			return w.connectionFailure(v, D.ErrUnavailable)
		}
	case "DELETING", "CLEANUP_PENDING":
		v.State = D.Stopping
		v.Stage = "deleting_environment"
		v.Reason = "environment_failed"
		v.CleanupError = env.State == "CLEANUP_PENDING"
	case "DELETED", "FAILED":
		v.Reason = "environment_failed"
		w.finish(v)
	default:
		return D.ErrProtocol
	}
	return nil
}

// agentReadError отличает отказ чтения агента от ошибки транзакции архива.
type agentReadError struct{ err error }

// Error возвращает безопасное текстовое описание ошибки.
func (e *agentReadError) Error() string { return "agent journal unavailable" }

// Unwrap раскрывает исходную ошибку для errors.Is и errors.As без потери классификации.
func (e *agentReadError) Unwrap() error { return e.err }

// collect архивирует доступные страницы журнала агента и продвигает курсор только после записи в базу.
func (w *Worker) collect(ctx context.Context, l D.Lease, v *D.Session) (bool, error) {
	for range 6 {
		if err := w.store.CheckLease(ctx, l); err != nil {
			return false, err
		}
		batch, err := w.agent.Events(ctx, v.ID, v.AgentSeq, D.EventLimits{Count: 100, Bytes: 16 << 20})
		if err != nil {
			return false, &agentReadError{err}
		}
		if len(batch) == 0 {
			return true, nil
		}
		if err = w.store.CommitEvents(ctx, l, batch); err != nil {
			if errors.Is(err, D.ErrProtocol) {
				return false, &agentReadError{err}
			}
			return false, err
		}
		v.AgentSeq = batch[len(batch)-1].Seq
		for _, ev := range batch {
			if ev.Generation != "" {
				v.Generation = ev.Generation
			}
			if ev.Kind == "session_state" && (ev.State == "STOPPED" || ev.State == "FAILED") {
				v.State = D.Stopping
				v.Stage = "stopping"
				v.Reason = reason(ev.Reason)
			}
		}
	}
	return false, nil
}

// connectionFailure переводит сессию в восстановление связи либо начинает очистку при исчерпании попыток или
// нарушении протокола.
func (w *Worker) connectionFailure(v *D.Session, err error) error {
	if errors.Is(err, D.ErrProtocol) {
		v.State = D.Stopping
		v.Stage = "stopping"
		v.Reason = "protocol_error"
		return nil
	}
	delays := []time.Duration{1, 2, 4, 8, 16}
	if v.Retry >= len(delays) {
		v.State = D.Stopping
		v.Stage = "stopping"
		v.Reason = "agent_unreachable"
		return nil
	}
	v.State = D.Reconnecting
	v.Stage = "reconnecting"
	v.NextCheck = w.now().Add(delays[v.Retry] * time.Second)
	v.Retry++
	return nil
}

// ready проверяет живое окружение и агента, передаёт активность, собирает события и отправляет следующую
// команду.
func (w *Worker) ready(ctx context.Context, l D.Lease, v *D.Session) error {
	if err := w.store.CheckLease(ctx, l); err != nil {
		return err
	}
	env, err := w.env.Get(ctx, v.ID)
	if errors.Is(err, D.ErrNotFound) || err == nil && (env.State == "DELETED" || env.State == "FAILED") {
		v.Reason = "environment_lost"
		v.JournalIncomplete = true
		v.JournalArchived = true
		if err = w.markUnknown(ctx, l); err != nil {
			return err
		}
		w.finish(v)
		return nil
	}
	if err != nil {
		return err
	}
	if env.State == "DELETING" || env.State == "CLEANUP_PENDING" {
		v.State = D.Stopping
		v.Stage = "deleting_environment"
		v.Reason = "environment_lost"
		v.JournalIncomplete = true
		return nil
	}
	h, err := w.agent.Health(ctx, v.ID)
	if err != nil {
		return w.connectionFailure(v, err)
	}
	if h.SessionID != v.ID || h.Language != v.Language {
		return w.connectionFailure(v, D.ErrProtocol)
	}
	if h.State == "STOPPED" || h.State == "FAILED" || h.Draining {
		v.State = D.Stopping
		v.Stage = "stopping"
		v.Reason = reason(h.Reason)
		return nil
	}
	if !h.Ready {
		return w.connectionFailure(v, D.ErrUnavailable)
	}
	if h.Generation != "" {
		v.Generation = h.Generation
	}
	if v.ActivityPending {
		if err = w.store.CheckLease(ctx, l); err != nil {
			return err
		}
		if err = w.agent.Activity(ctx, v.ID); err != nil {
			return w.connectionFailure(v, err)
		}
		v.ActivityPending = false
	}
	_, err = w.collect(ctx, l, v)
	if err != nil {
		var network *agentReadError
		if errors.As(err, &network) {
			return w.connectionFailure(v, err)
		}
		return err
	}
	if v.State == D.Stopping {
		return nil
	}
	v.State = D.Ready
	if err = w.dispatch(ctx, l, v); err != nil {
		return err
	}
	if v.State != D.Reconnecting && v.State != D.Stopping {
		v.State = D.Ready
		v.Stage = "ready"
		v.Retry = 0
	}
	return nil
}

// dispatch отправляет одну ожидающую команду с прежним ID; при заполнении агента оставляет её в очереди
// backend.
func (w *Worker) dispatch(ctx context.Context, l D.Lease, v *D.Session) error {
	xs, err := w.store.Executions(ctx, v.ID)
	if err != nil {
		return err
	}
	for _, x := range xs {
		if x.Dispatch != "PENDING" {
			continue
		}
		if err = w.store.CheckLease(ctx, l); err != nil {
			return err
		}
		reply, callErr := w.agent.Execute(ctx, v.ID, D.Command{ExecutionID: x.ID, Code: x.Code, TimeoutSeconds: x.TimeoutSeconds, Kind: x.Kind})
		switch {
		case errors.Is(callErr, D.ErrQueueFull):
			return nil // Сохраняем команду в очереди backend до освобождения места у агента.
		case errors.Is(callErr, D.ErrInvalid), errors.Is(callErr, D.ErrConflict):
			x.State = "REJECTED"
			x.Dispatch = "REJECTED"
		case callErr != nil:
			return w.connectionFailure(v, callErr)
		default:
			// Повтор запроса может вернуть завершённое состояние до обработки событий вывода.
			// Сначала архивируем доступные события и заново читаем накопленный результат,
			// чтобы подтверждение завершения не затёрло вывод пустым снимком.
			if terminal := (D.Execution{State: reply.State}); terminal.Terminal() {
				complete, collectErr := w.collect(ctx, l, v)
				if collectErr != nil {
					var readErr *agentReadError
					if errors.As(collectErr, &readErr) {
						return w.connectionFailure(v, collectErr)
					}
					return collectErr
				}
				if !complete {
					return nil
				}
				x, err = w.store.Execution(ctx, v.OwnerID, v.ID, x.ID)
				if err != nil {
					return err
				}
			}
			x.Dispatch = "ACCEPTED"
			x.State = reply.State
			x.Result = reply.Result
			v.LastActivityAt = w.now()
		}
		if err = w.store.SaveExecution(ctx, l, x); err != nil {
			return err
		}
		break
	}
	return nil
}

// markUnknown сохраняет недостоверный исход незавершённых попыток, не запуская их автоматически повторно.
func (w *Worker) markUnknown(ctx context.Context, l D.Lease) error {
	xs, err := w.store.Executions(ctx, l.Session.ID)
	if err != nil {
		return err
	}
	for _, x := range xs {
		if x.Terminal() {
			continue
		}
		x.State = "UNKNOWN"
		x.Dispatch = "ACCEPTED"
		if err = w.store.SaveExecution(ctx, l, x); err != nil {
			return err
		}
	}
	return nil
}

// cleanup останавливает приём команд, сохраняет доступные результаты и запрашивает идемпотентное удаление
// окружения.
func (w *Worker) cleanup(ctx context.Context, l D.Lease, v *D.Session) error {
	if err := w.store.CheckLease(ctx, l); err != nil {
		return err
	}
	env, err := w.env.Get(ctx, v.ID)
	if errors.Is(err, D.ErrNotFound) {
		if v.CreateUnknown {
			env, err = w.env.RequestCreate(ctx, D.EnvironmentRequest{SessionID: v.ID, Language: v.Language, Profile: "default"})
			if err != nil {
				return err
			}
			v.CreateUnknown = false
		} else {
			if v.AgentStarted {
				v.JournalIncomplete = true
			}
			if err = w.markUnknown(ctx, l); err != nil {
				return err
			}
			w.finish(v)
			return nil
		}
	} else if err != nil {
		return err
	}
	if env.State == "DELETED" || env.State == "FAILED" {
		if v.AgentStarted && !v.JournalArchived {
			v.JournalIncomplete = true
		}
		if err = w.markUnknown(ctx, l); err != nil {
			return err
		}
		w.finish(v)
		return nil
	}
	if !v.JournalArchived && v.AgentStarted {
		if err = w.store.CheckLease(ctx, l); err != nil {
			return err
		}
		_ = w.agent.Drain(ctx, v.ID)
		if err = w.store.CheckLease(ctx, l); err != nil {
			return err
		}
		stopErr := w.agent.Stop(ctx, v.ID)
		if stopErr != nil {
			v.StopRetry++
		}
		empty, archiveErr := w.collect(ctx, l, v)
		if archiveErr != nil {
			var network *agentReadError
			if !errors.As(archiveErr, &network) {
				return archiveErr
			}
			v.JournalRetry++
			if v.JournalRetry < 6 {
				return nil
			}
			v.JournalIncomplete = true
		} else {
			v.JournalRetry = 0
			if !empty {
				return nil
			}
			if stopErr != nil && v.StopRetry < 6 {
				return nil
			}
			if stopErr != nil {
				v.JournalIncomplete = true
			}
		}
	}
	if err = w.markUnknown(ctx, l); err != nil {
		return err
	}
	v.JournalArchived = true
	if err = w.store.Checkpoint(ctx, l, *v); err != nil {
		return err
	}
	if err = w.store.CheckLease(ctx, l); err != nil {
		return err
	}
	if !v.DeleteSent && env.State != "DELETING" && env.State != "CLEANUP_PENDING" {
		if err = w.env.RequestDelete(ctx, v.ID, reason(v.Reason)); err != nil {
			v.CleanupError = true
			v.Stage = "cleanup_pending"
			return nil
		}
	}
	v.DeleteSent = true
	v.Stage = "deleting_environment"
	v.CleanupError = env.State == "CLEANUP_PENDING"
	if v.CleanupError {
		v.Stage = "cleanup_pending"
	}
	return nil
}
