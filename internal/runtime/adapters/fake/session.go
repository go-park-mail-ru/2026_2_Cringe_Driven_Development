package fake

import (
	"context"
	"encoding/json"

	U "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
)

// ExecuteCommand имитирует идемпотентное принятие кода или инспекции, сохраняя команду и результат в памяти.
func (a *Agent) ExecuteCommand(ctx context.Context, ep U.AgentEndpoint, c D.Command) (D.AgentExecution, error) {
	if _, err := D.CommandPayload(c); err != nil {
		return D.AgentExecution{}, err
	}
	a.mu.Lock()
	s, _, err := a.call(ctx, "ExecuteCommand", ep)
	if s == nil {
		a.mu.Unlock()
		if err != nil {
			return D.AgentExecution{}, err
		}
		return D.AgentExecution{}, D.ErrUnavailable
	}
	if s.state != "READY" {
		a.mu.Unlock()
		return D.AgentExecution{}, D.ErrConflict
	}
	payload, _ := json.Marshal(struct {
		D.Command
		Kind string
	}{c, c.Kind})
	if s.commands == nil {
		s.commands = map[string]string{}
	}
	if previous, ok := s.commands[c.ExecutionID]; ok {
		a.mu.Unlock()
		if previous != string(payload) {
			return D.AgentExecution{}, D.ErrConflict
		}
		return a.ReadExecution(ctx, ep, c.ExecutionID)
	}
	s.commands[c.ExecutionID] = string(payload)
	if c.Kind == "inspect" {
		if s.results == nil {
			s.results = map[string]json.RawMessage{}
		}
		result := json.RawMessage(`{"variables":[]}`)
		s.results[c.ExecutionID] = result
		s.executions[c.ExecutionID] = "SUCCEEDED"
		raw, marshalErr := json.Marshal(map[string]any{"seq": len(s.events) + 1, "session_id": s.session.String(), "kind": "execution_state", "execution_id": c.ExecutionID, "state": "SUCCEEDED", "result": result})
		if marshalErr != nil {
			a.mu.Unlock()
			return D.AgentExecution{}, marshalErr
		}
		s.events = append(s.events, raw)
		a.mu.Unlock()
		return D.AgentExecution{ExecutionID: c.ExecutionID, State: "SUCCEEDED", Result: result}, nil
	}
	a.mu.Unlock()
	if err = a.Execute(ctx, ep, c.ExecutionID, c.Code); err != nil {
		return D.AgentExecution{}, err
	}
	return a.ReadExecution(ctx, ep, c.ExecutionID)
}

// ReadExecution возвращает состояние и результат команды из памяти тестового агента.
func (a *Agent) ReadExecution(ctx context.Context, ep U.AgentEndpoint, id string) (D.AgentExecution, error) {
	state, err := a.Execution(ctx, ep, id)
	if err != nil {
		return D.AgentExecution{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.agents[ep.BaseURL]
	var result json.RawMessage
	if s != nil {
		result = append(json.RawMessage(nil), s.results[id]...)
	}
	return D.AgentExecution{ExecutionID: id, State: state, Result: result}, nil
}

// ReadEvents возвращает события тестового агента после курсора с ограничением размера страницы.
func (a *Agent) ReadEvents(ctx context.Context, ep U.AgentEndpoint, after int64, limits D.EventLimits) ([]D.AgentEvent, error) {
	raw, err := a.Events(ctx, ep, int(after))
	if err != nil {
		return nil, err
	}
	if limits.Count <= 0 || limits.Count > 100 {
		limits.Count = 100
	}
	if limits.Bytes <= 0 {
		limits.Bytes = 16 << 20
	}
	out := []D.AgentEvent{}
	size := 0
	for _, b := range raw {
		if len(out) >= limits.Count || (len(out) > 0 && size+len(b) > limits.Bytes) {
			break
		}
		var ev D.AgentEvent
		if err = json.Unmarshal(b, &ev); err != nil {
			return nil, err
		}
		out = append(out, ev)
		size += len(b)
	}
	return out, nil
}

// Activity проверяет доступность тестового агента без реального обращения к VPS.
func (a *Agent) Activity(ctx context.Context, ep U.AgentEndpoint) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, status, err := a.call(ctx, "Activity", ep)
	if err != nil {
		return err
	}
	if s == nil || status != 200 || s.state != "READY" {
		return D.ErrConflict
	}
	return nil
}

// Interrupt имитирует принятие запроса прерывания проверкой готовности тестового агента.
func (a *Agent) Interrupt(ctx context.Context, ep U.AgentEndpoint) error { return a.Activity(ctx, ep) }

// Restart меняет поколение тестового kernel и добавляет событие kernel_started в журнал.
func (a *Agent) Restart(ctx context.Context, ep U.AgentEndpoint) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, status, err := a.call(ctx, "Restart", ep)
	if err != nil {
		return "", err
	}
	if s == nil || status != 200 || s.state != "READY" {
		return "", D.ErrUnavailable
	}
	s.generation = uuid.NewString()
	raw, err := json.Marshal(map[string]any{"seq": len(s.events) + 1, "session_id": s.session.String(), "kind": "kernel_started", "generation": s.generation})
	if err != nil {
		return "", err
	}
	s.events = append(s.events, raw)
	return s.generation, nil
}
