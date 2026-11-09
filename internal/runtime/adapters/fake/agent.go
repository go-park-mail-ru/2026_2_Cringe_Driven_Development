package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sync"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"github.com/google/uuid"
)

// ErrUnreachable — по адресу нет зарегистрированного агента.
var ErrUnreachable = errors.New("fake agent: connection refused")

var printCall = regexp.MustCompile(`^\s*print\((.*)\)\s*$`)

// agentState хранит состояние тестового агента в памяти; настоящую VPS и kernel не создаёт.
type agentState struct {
	// token Секретный токен только для внутреннего обращения к агенту.
	token, language string
	// generation Текущее поколение тестового kernel.
	generation string
	// results Подготовленные результаты тестового агента.
	results map[string]json.RawMessage
	// session Снимок тестовой сессии.
	session uuid.UUID
	// state Состояние тестового агента.
	state string
	// status Состояние, подставляемое HTTP-тестом.
	status int // Значение 0 выбирает ответ по текущему состоянию.
	// executions Состояния команд тестового агента.
	executions map[string]string
	// commands Команды, сохранённые тестовым агентом.
	commands map[string]string
	// events Журнал событий тестового агента.
	events []json.RawMessage
}

// Agent — in-memory usecase.AgentClient. Агенты адресуются по AgentEndpoint.BaseURL.
//
// FailNext[метод] — ошибка следующего вызова (Health, Assign, Drain, Stop, Execute, Execution, Events).
// Health: незарегистрированный адрес → ErrUnreachable; неверный токен → 401; после Drain/Stop или
// SetStatus(≠200) → ready=false.
type Agent struct {
	FailNext map[string]error

	mu     sync.Mutex
	agents map[string]*agentState
	calls  map[string]int
}

// NewAgent создаёт агента без зарегистрированных сессий.
func NewAgent() *Agent {
	return &Agent{FailNext: map[string]error{}, agents: map[string]*agentState{}, calls: map[string]int{}}
}

// RegisterWaiting запускает «агента» без сессии по адресу baseURL: /health без токена отвечает
// WAITING, сессию назначает Assign.
func (a *Agent) RegisterWaiting(baseURL string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.agents[baseURL] = &agentState{state: "WAITING", executions: map[string]string{}}
}

// Assign назначает ждущего агента сессии один раз: повтор с той же сессией и токеном — тот же ответ,
// другая сессия или токен — 409, язык не python — 400.
func (a *Agent) Assign(ctx context.Context, ep usecase.AgentEndpoint, sessionID uuid.UUID, language string) (usecase.AgentHealth, int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls["Assign"]++
	if err := take(a.FailNext, "Assign"); err != nil {
		return usecase.AgentHealth{}, 0, err
	}
	if err := ctx.Err(); err != nil {
		return usecase.AgentHealth{}, 0, err
	}
	s, ok := a.agents[ep.BaseURL]
	switch {
	case !ok:
		return usecase.AgentHealth{}, 0, fmt.Errorf("%w: %s", ErrUnreachable, ep.BaseURL)
	case language != "python":
		return usecase.AgentHealth{}, http.StatusBadRequest, nil
	case s.state == "WAITING":
		s.session, s.token, s.language, s.state = sessionID, ep.Token, language, "READY"
	case s.session != sessionID || s.token != ep.Token:
		return usecase.AgentHealth{}, http.StatusConflict, nil
	}
	return a.snapshotLocked(s)
}

// Register запускает «агента», уже назначенного сессии, по адресу baseURL с токеном token.
func (a *Agent) Register(baseURL, token string, session uuid.UUID, language string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.agents[baseURL] = &agentState{
		token: token, language: language, session: session, state: "READY", executions: map[string]string{},
	}
}

// SetStatus задаёт HTTP-статус ответа /health агента по адресу baseURL (0 — по состоянию).
func (a *Agent) SetStatus(baseURL string, status int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.agents[baseURL]; ok {
		s.status = status
	}
}

// SetState задаёт состояние сессии агента по адресу baseURL (например, FAILED после неудачного старта kernel).
func (a *Agent) SetState(baseURL, state string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.agents[baseURL]; ok {
		s.state = state
	}
}

// Fail потокобезопасно задаёт ошибку следующего вызова method.
func (a *Agent) Fail(method string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.FailNext[method] = err
}

// Calls возвращает число вызовов метода.
func (a *Agent) Calls(method string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls[method]
}

// call учитывает вызов и находит агента; вызывающий держит a.mu и освобождает его.
func (a *Agent) call(ctx context.Context, method string, ep usecase.AgentEndpoint) (*agentState, int, error) {
	a.calls[method]++
	if err := take(a.FailNext, method); err != nil {
		return nil, 0, err
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	s, ok := a.agents[ep.BaseURL]
	if !ok {
		return nil, 0, fmt.Errorf("%w: %s", ErrUnreachable, ep.BaseURL)
	}
	if s.state == "WAITING" {
		if method == "Health" {
			return s, http.StatusOK, nil
		}
		return nil, http.StatusServiceUnavailable, nil
	}
	if ep.Token != s.token {
		return nil, http.StatusUnauthorized, nil
	}
	return s, http.StatusOK, nil
}

// Health возвращает готовность, язык и поколение тестового агента из памяти.
func (a *Agent) Health(ctx context.Context, ep usecase.AgentEndpoint) (usecase.AgentHealth, int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, status, err := a.call(ctx, "Health", ep)
	if s == nil {
		return usecase.AgentHealth{}, status, err
	}
	return a.snapshotLocked(s)
}

// snapshotLocked — ответ /health и /assign по состоянию агента; вызывающий держит a.mu.
func (a *Agent) snapshotLocked(s *agentState) (usecase.AgentHealth, int, error) {
	if s.state == "WAITING" {
		return usecase.AgentHealth{State: "WAITING", Ready: true}, http.StatusOK, nil
	}
	h := usecase.AgentHealth{SessionID: s.session.String(), Language: s.language, State: s.state, Ready: s.state == "READY", Generation: s.generation}
	status := http.StatusOK
	switch {
	case s.status != 0:
		status = s.status
	case !h.Ready:
		status = http.StatusServiceUnavailable
	}
	h.Ready = h.Ready && status == http.StatusOK
	return h, status, nil
}

func (a *Agent) transition(ctx context.Context, method string, ep usecase.AgentEndpoint, state string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, status, err := a.call(ctx, method, ep)
	if s == nil {
		if err == nil {
			err = fmt.Errorf("fake agent %s: HTTP %d", method, status)
		}
		return err
	}
	s.state = state
	return nil
}

// Drain переводит агента в DRAINING.
func (a *Agent) Drain(ctx context.Context, ep usecase.AgentEndpoint) error {
	return a.transition(ctx, "Drain", ep, "DRAINING")
}

// Stop переводит агента в STOPPED.
func (a *Agent) Stop(ctx context.Context, ep usecase.AgentEndpoint) error {
	return a.transition(ctx, "Stop", ep, "STOPPED")
}

// Execute сразу «выполняет» код: для print(x) добавляет событие stream stdout "x\n".
func (a *Agent) Execute(ctx context.Context, ep usecase.AgentEndpoint, executionID, code string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, status, err := a.call(ctx, "Execute", ep)
	if s == nil {
		if err == nil {
			err = fmt.Errorf("fake agent Execute: HTTP %d", status)
		}
		return err
	}
	if _, ok := s.executions[executionID]; ok {
		return nil
	}
	s.executions[executionID] = "SUCCEEDED"
	if m := printCall.FindStringSubmatch(code); m != nil {
		ev, err := json.Marshal(map[string]any{
			"seq":          len(s.events) + 1,
			"session_id":   s.session.String(),
			"kind":         "kernel_message",
			"execution_id": executionID,
			"channel":      "iopub",
			"msg_type":     "stream",
			"content":      map[string]string{"name": "stdout", "text": m[1] + "\n"},
		})
		if err != nil {
			return err
		}
		s.events = append(s.events, ev)
	}
	return nil
}

// Execution возвращает состояние выполнения (всегда SUCCEEDED для известных).
func (a *Agent) Execution(ctx context.Context, ep usecase.AgentEndpoint, id string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, status, err := a.call(ctx, "Execution", ep)
	if s == nil {
		if err == nil {
			err = fmt.Errorf("fake agent Execution: HTTP %d", status)
		}
		return "", err
	}
	state, ok := s.executions[id]
	if !ok {
		return "", fmt.Errorf("fake agent Execution %s: HTTP %d", id, http.StatusNotFound)
	}
	return state, nil
}

// Events возвращает события с seq > after.
func (a *Agent) Events(ctx context.Context, ep usecase.AgentEndpoint, after int) ([]json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, status, err := a.call(ctx, "Events", ep)
	if s == nil {
		if err == nil {
			err = fmt.Errorf("fake agent Events: HTTP %d", status)
		}
		return nil, err
	}
	if after < 0 {
		after = 0
	}
	if after >= len(s.events) {
		return []json.RawMessage{}, nil
	}
	return append([]json.RawMessage(nil), s.events[after:]...), nil
}
