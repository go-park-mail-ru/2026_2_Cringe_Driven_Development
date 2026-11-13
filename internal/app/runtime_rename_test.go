package app

import (
	"context"
	"sync"
	"sync/atomic"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	UC "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/usecase"
	"github.com/google/uuid"
)

// renameSessionAgent удерживает завершение команды run, пока тест переименовывает блокнот.
type renameSessionAgent struct {
	UC.SessionAgent
	release  chan struct{}
	once     sync.Once
	controls atomic.Int64
	mu       sync.Mutex
	terminal D.AgentEvent
}

func (a *renameSessionAgent) finish() { a.once.Do(func() { close(a.release) }) }

func (a *renameSessionAgent) held(id string) bool {
	if id != "run" {
		return false
	}
	select {
	case <-a.release:
		return false
	default:
		return true
	}
}

func (a *renameSessionAgent) Execute(ctx context.Context, sid uuid.UUID, cmd D.Command) (D.AgentExecution, error) {
	x, err := a.SessionAgent.Execute(ctx, sid, cmd)
	if err == nil && a.held(cmd.ExecutionID) {
		// Штатный fake возвращает SUCCEEDED сразу и пишет только stream.
		// Обёртка публикует это подтверждение событием после снятия барьера.
		events, eventErr := a.SessionAgent.Events(ctx, sid, 0, D.EventLimits{Count: 100, Bytes: 16 << 20})
		if eventErr != nil {
			return D.AgentExecution{}, eventErr
		}
		seq := int64(1)
		if len(events) > 0 {
			seq = events[len(events)-1].Seq + 1
		}
		a.mu.Lock()
		a.terminal = D.AgentEvent{Seq: seq, SessionID: sid, ExecutionID: cmd.ExecutionID, Kind: "execution_state", State: x.State, Result: x.Result}
		a.mu.Unlock()
		x.State = "RUNNING"
	}
	return x, err
}

func (a *renameSessionAgent) Execution(ctx context.Context, sid uuid.UUID, id string) (D.AgentExecution, error) {
	x, err := a.SessionAgent.Execution(ctx, sid, id)
	if err == nil && a.held(id) {
		x.State = "RUNNING"
	}
	return x, err
}

func (a *renameSessionAgent) Events(ctx context.Context, sid uuid.UUID, after int64, limits D.EventLimits) ([]D.AgentEvent, error) {
	a.mu.Lock()
	terminal := a.terminal
	a.mu.Unlock()
	sourceAfter := after
	if terminal.SessionID == sid && after >= terminal.Seq {
		sourceAfter--
	}
	events, err := a.SessionAgent.Events(ctx, sid, sourceAfter, limits)
	if err != nil || terminal.SessionID != sid {
		return events, err
	}
	out := make([]D.AgentEvent, 0, len(events)+1)
	for _, ev := range events {
		if a.held(ev.ExecutionID) {
			return out, nil
		}
		if ev.Seq >= terminal.Seq {
			if after < terminal.Seq {
				out = append(out, terminal)
				after = terminal.Seq
			}
			ev.Seq++
		}
		out = append(out, ev)
	}
	if !a.held(terminal.ExecutionID) && after < terminal.Seq && (len(events) == 0 || events[len(events)-1].Seq >= terminal.Seq-1) {
		out = append(out, terminal)
	}
	if limits.Count > 0 && len(out) > limits.Count {
		out = out[:limits.Count]
	}
	return out, nil
}

func (a *renameSessionAgent) Drain(ctx context.Context, sid uuid.UUID) error {
	a.controls.Add(1)
	return a.SessionAgent.Drain(ctx, sid)
}

func (a *renameSessionAgent) Stop(ctx context.Context, sid uuid.UUID) error {
	a.controls.Add(1)
	return a.SessionAgent.Stop(ctx, sid)
}

func (a *renameSessionAgent) Interrupt(ctx context.Context, sid uuid.UUID) error {
	a.controls.Add(1)
	return a.SessionAgent.(UC.KernelAgent).Interrupt(ctx, sid)
}

func (a *renameSessionAgent) Restart(ctx context.Context, sid uuid.UUID) (string, error) {
	a.controls.Add(1)
	return a.SessionAgent.(UC.KernelAgent).Restart(ctx, sid)
}
