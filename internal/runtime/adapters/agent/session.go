package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
)

// sessionStatus сопоставляет HTTP-отказ агента безопасной ошибке runtime-модуля.
func sessionStatus(status int) error {
	switch status {
	case 400, 422:
		return D.ErrInvalid
	case 404:
		return D.ErrNotFound
	case 409:
		return D.ErrConflict
	case 429:
		return D.ErrQueueFull
	default:
		return D.ErrUnavailable
	}
}

// ExecuteCommand передаёт агенту исполнение кода или инспекцию с неизменным execution_id и проверяет
// подтверждение.
func (c *Client) ExecuteCommand(ctx context.Context, ep usecase.AgentEndpoint, cmd D.Command) (D.AgentExecution, error) {
	if _, err := D.CommandPayload(cmd); err != nil {
		return D.AgentExecution{}, err
	}
	path := "/executions"
	var body any = cmd
	if cmd.Kind == "inspect" {
		if err := (D.InspectRequest{ExecutionID: cmd.ExecutionID}).Validate(); err != nil {
			return D.AgentExecution{}, err
		}
		path = "/variables"
		body = D.InspectRequest{ExecutionID: cmd.ExecutionID}
	}
	status, raw, err := c.do(ctx, ep, http.MethodPost, path, body)
	if err != nil {
		return D.AgentExecution{}, err
	}
	if status != 202 {
		return D.AgentExecution{}, sessionStatus(status)
	}
	var out D.AgentExecution
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, D.ErrProtocol
	}
	if out.ExecutionID != cmd.ExecutionID || out.State == "" {
		return out, D.ErrProtocol
	}
	return out, nil
}

// ReadExecution запрашивает состояние и результат конкретной команды агента по её ID.
func (c *Client) ReadExecution(ctx context.Context, ep usecase.AgentEndpoint, id string) (D.AgentExecution, error) {
	status, raw, err := c.do(ctx, ep, http.MethodGet, "/executions/"+url.PathEscape(id), nil)
	if err != nil {
		return D.AgentExecution{}, err
	}
	if status != 200 {
		return D.AgentExecution{}, sessionStatus(status)
	}
	var out D.AgentExecution
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, D.ErrProtocol
	}
	if out.ExecutionID != id || out.State == "" {
		return out, D.ErrProtocol
	}
	return out, nil
}

// ReadEvents получает следующую страницу событий агента, сохраняя порядок и целостность большого первого
// события.
func (c *Client) ReadEvents(ctx context.Context, ep usecase.AgentEndpoint, after int64, limits D.EventLimits) ([]D.AgentEvent, error) {
	if after < 0 {
		return nil, D.ErrInvalid
	}
	status, raw, err := c.do(ctx, ep, http.MethodGet, fmt.Sprintf("/events?after=%d", after), nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, sessionStatus(status)
	}
	var page struct {
		Events []D.AgentEvent `json:"events"`
	}
	if err = json.Unmarshal(raw, &page); err != nil {
		return nil, D.ErrProtocol
	}
	if len(page.Events) > 100 {
		return nil, D.ErrProtocol
	}
	if limits.Count <= 0 || limits.Count > 100 {
		limits.Count = 100
	}
	if limits.Bytes <= 0 {
		limits.Bytes = 16 << 20
	}
	out := make([]D.AgentEvent, 0)
	size := 0
	cursor := after
	for _, ev := range page.Events {
		if ev.Seq <= cursor {
			return nil, D.ErrProtocol
		}
		cursor = ev.Seq
		b, err := json.Marshal(ev)
		if err != nil {
			return nil, D.ErrProtocol
		}
		if len(out) >= limits.Count || (len(out) > 0 && size+len(b) > limits.Bytes) {
			break
		}
		size += len(b)
		out = append(out, ev)
	}
	return out, nil
}

// Activity передаёт агенту реальное действие пользователя, продлевающее время активности.
func (c *Client) Activity(ctx context.Context, ep usecase.AgentEndpoint) error {
	return c.post(ctx, ep, "/runtime/activity", http.StatusOK)
}

// Interrupt запрашивает прерывание текущей команды без автоматического повтора при потере ответа.
func (c *Client) Interrupt(ctx context.Context, ep usecase.AgentEndpoint) error {
	return c.post(ctx, ep, "/kernel/interrupt", http.StatusOK)
}

// Restart перезапускает свободный kernel и возвращает новое поколение; потерянный ответ автоматически не
// повторяется.
func (c *Client) Restart(ctx context.Context, ep usecase.AgentEndpoint) (string, error) {
	status, raw, err := c.do(ctx, ep, http.MethodPost, "/kernel/restart", nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", sessionStatus(status)
	}
	var out struct {
		Generation string `json:"generation"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return "", D.ErrProtocol
	}
	if _, err = uuid.Parse(out.Generation); err != nil {
		return "", D.ErrProtocol
	}
	return out.Generation, nil
}
