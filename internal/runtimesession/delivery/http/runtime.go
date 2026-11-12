// Package delivery подключает runtime к общему StrictServer и проверенному principal.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/principal"
	"github.com/google/uuid"
)

// Usecase определяет сценарии, которые HTTP-хендлер вызывает после проверки авторизации.
type Usecase interface {
	// Queue читает только незавершённые исполнения ячеек: выполняемое первым, затем ожидающие; инспекции не
	// включаются.
	Queue(context.Context, int64, uuid.UUID) (D.Queue, error)
	// ExecuteBatch проверяет запрос Run all, задаёт таймаут по умолчанию и передаёт атомарное принятие пакета
	// хранилищу.
	ExecuteBatch(context.Context, int64, uuid.UUID, D.BatchRequest) (D.Batch, error)
	// Inspect проверяет ID команды и сохраняет инспекцию до её отправки агенту.
	Inspect(context.Context, int64, uuid.UUID, D.InspectRequest) (D.Inspection, error)
	// GetInspection читает сохранённый результат инспекции; кодовое исполнение с тем же ID инспекцией не
	// считается.
	GetInspection(context.Context, int64, uuid.UUID, string) (D.Inspection, error)
	// Kernel выполняет ограниченный по времени interrupt или restart под общей блокировкой приёма команд.
	Kernel(context.Context, int64, uuid.UUID, bool) (string, error)
	// Start проверяет параметры и сохраняет намерение запуска пользовательской сессии до подготовки VPS.
	Start(context.Context, int64, int64, D.StartRequest) (D.Session, error)
	// GetActive читает активную runtime-сессию пользователя; ошибка хранилища не подменяется её отсутствием.
	GetActive(context.Context, int64) (*D.Session, error)
	// GetSession возвращает сессию только после проверки её принадлежности пользователю.
	GetSession(context.Context, int64, uuid.UUID) (D.Session, error)
	// Execute передаёт сохранённую команду агенту; неизменный ID позволяет безопасно повторить сетевой запрос.
	Execute(context.Context, int64, uuid.UUID, D.ExecuteRequest) (D.Execution, error)
	// GetExecution возвращает исполнение ячейки владельцу, исключая служебные инспекции из этого API.
	GetExecution(context.Context, int64, uuid.UUID, string) (D.Execution, error)
	// Events возвращает события после указанного курсора; чтение не продлевает активность пользователя.
	Events(context.Context, int64, uuid.UUID, int64) (D.EventPage, error)
	// Activity передаёт агенту реальное действие пользователя, продлевающее время активности.
	Activity(context.Context, int64, uuid.UUID) error
	// Stop сохраняет намерение завершить сессию; фактическую остановку и очистку выполняет фоновый обработчик.
	Stop(context.Context, int64, uuid.UUID) error
}

// Handler переводит пользовательские HTTP-запросы в сценарии runtime и собирает ответы по контракту spec/openapi.json.
type Handler struct{ uc Usecase }

// NewHandler подключает сценарии runtime-сессий к сгенерированному HTTP-интерфейсу.
func NewHandler(uc Usecase) *Handler { return &Handler{uc} }

// owner извлекает ID пользователя из контекста, ранее проверенного middleware авторизации.
func owner(ctx context.Context) int64 { id, _ := principal.UserIDFromContext(ctx); return id }

// session создаёт публичное представление сессии без токенов, адресов агента и облачных идентификаторов.
func session(v D.Session) api.RuntimeSession {
	out := api.RuntimeSession{Id: v.ID, NotebookId: v.NotebookID, Language: api.RuntimeSessionLanguage(v.Language), State: api.RuntimeSessionState(v.State), Stage: api.RuntimeSessionStage(v.Stage), CreatedAt: v.CreatedAt, LastActivityAt: v.LastActivityAt, PublicSeq: v.PublicSeq, JournalIncomplete: v.JournalIncomplete}
	if v.Reason != "" {
		out.Reason = &v.Reason
	}
	if v.CleanupError {
		code := "cleanup_pending"
		out.CleanupError = &code
	}
	if id, err := uuid.Parse(v.Generation); err == nil {
		out.Generation = &id
	}
	return out
}

// execution создаёт ответ об исполнении, раскрывая выводы и скрывая исходник команды и служебные поля.
func execution(v D.Execution) (api.RuntimeExecution, error) {
	out := api.RuntimeExecution{ExecutionId: v.ID, SessionId: v.SessionID, CellId: v.CellID, SourceHash: v.SourceHash, TimeoutSeconds: float32(v.TimeoutSeconds), Attempt: v.Attempt, State: api.RuntimeExecutionState(v.State), Outputs: make([]map[string]interface{}, 0, len(v.Outputs)), ExecutionCount: v.ExecutionCount, OutputTruncated: v.Truncated, Projection: api.RuntimeExecutionProjection(v.Projection)}
	if v.State == "PENDING" {
		out.State = "QUEUED"
	}
	for _, b := range v.Outputs {
		var fields map[string]interface{}
		if err := json.Unmarshal(b, &fields); err != nil {
			return out, err
		}
		out.Outputs = append(out.Outputs, fields)
	}
	return out, nil
}

// failure объединяет HTTP-статус и безопасное тело ошибки для ответов runtime API.
type failure struct {
	// status Состояние, подставляемое HTTP-тестом.
	status int
	// body Подготовленное тело тестового ответа.
	body any
}

// write записывает согласованный HTTP-статус и безопасное JSON-тело ошибки.
func (f *failure) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.status)
	return json.NewEncoder(w).Encode(f.body)
}

// failureFor сопоставляет ошибки сценариев HTTP-ответам; конфликт активной сессии дополнительно содержит её
// данные.
func failureFor(err error) (*failure, error) {
	if err == nil {
		return nil, nil
	}
	var active *D.ActiveSessionError
	if errors.As(err, &active) {
		return &failure{409, api.RuntimeActiveSessionConflict{Code: "active_session_exists", Message: "Another runtime session is active", ActiveSession: session(active.Session)}}, nil
	}
	status, code, message := 0, "", ""
	switch {
	case errors.Is(err, D.ErrInvalid):
		status, code, message = 400, "validation_error", "Invalid runtime request"
	case errors.Is(err, D.ErrNotFound):
		status, code, message = 404, "not_found", "Runtime not found"
	case errors.Is(err, D.ErrConflict):
		status, code, message = 409, "conflict", "Runtime conflict"
	case errors.Is(err, D.ErrQueueFull):
		status, code, message = 429, "queue_full", "Runtime queue is full"
	case errors.Is(err, D.ErrUnavailable):
		status, code, message = 503, "runtime_unavailable", "Runtime unavailable"
	default:
		return nil, err
	}
	return &failure{status, api.RuntimeError{Code: api.RuntimeErrorCode(code), Message: message}}, nil
}

// StartRuntimeSession принимает запрос подготовки сессии для блокнота текущего пользователя и возвращает
// 202.
func (h *Handler) StartRuntimeSession(ctx context.Context, r api.StartRuntimeSessionRequestObject) (api.StartRuntimeSessionResponseObject, error) {
	if r.Body == nil || r.Id <= 0 {
		return failureFor(D.ErrInvalid)
	}
	v, err := h.uc.Start(ctx, owner(ctx), r.Id, D.StartRequest{RequestID: r.Body.RequestId, Language: string(r.Body.Language)})
	if err != nil {
		return failureFor(err)
	}
	return api.StartRuntimeSession202JSONResponse(session(v)), nil
}

// GetActiveRuntimeSession возвращает активную сессию пользователя или active_session:null.
func (h *Handler) GetActiveRuntimeSession(ctx context.Context, _ api.GetActiveRuntimeSessionRequestObject) (api.GetActiveRuntimeSessionResponseObject, error) {
	v, err := h.uc.GetActive(ctx, owner(ctx))
	if err != nil {
		return failureFor(err)
	}
	out := api.GetActiveRuntimeSession200JSONResponse{}
	if v != nil {
		s := session(*v)
		out.ActiveSession = &s
	}
	return out, nil
}

// GetRuntimeSession возвращает состояние, этап и безопасную причину завершения сессии владельца.
func (h *Handler) GetRuntimeSession(ctx context.Context, r api.GetRuntimeSessionRequestObject) (api.GetRuntimeSessionResponseObject, error) {
	v, err := h.uc.GetSession(ctx, owner(ctx), r.Id)
	if err != nil {
		return failureFor(err)
	}
	return api.GetRuntimeSession200JSONResponse(session(v)), nil
}

// CreateRuntimeExecution принимает исполнение сохранённой ячейки с проверкой версии исходника и возвращает
// 202.
func (h *Handler) CreateRuntimeExecution(ctx context.Context, r api.CreateRuntimeExecutionRequestObject) (api.CreateRuntimeExecutionResponseObject, error) {
	if r.Body == nil {
		return failureFor(D.ErrInvalid)
	}
	timeout := float64(300)
	if r.Body.TimeoutSeconds != nil {
		timeout = float64(*r.Body.TimeoutSeconds)
		if timeout <= 0 {
			return failureFor(D.ErrInvalid)
		}
	}
	v, err := h.uc.Execute(ctx, owner(ctx), r.Id, D.ExecuteRequest{ExecutionID: r.Body.ExecutionId, CellID: r.Body.CellId, SourceHash: r.Body.SourceHash, TimeoutSeconds: timeout})
	if err != nil {
		return failureFor(err)
	}
	out, err := execution(v)
	if err != nil {
		return nil, err
	}
	return api.CreateRuntimeExecution202JSONResponse(out), nil
}

// GetRuntimeExecution возвращает состояние и вывод одного исполнения без загрузки всего блокнота.
func (h *Handler) GetRuntimeExecution(ctx context.Context, r api.GetRuntimeExecutionRequestObject) (api.GetRuntimeExecutionResponseObject, error) {
	v, err := h.uc.GetExecution(ctx, owner(ctx), r.Id, r.ExecutionId)
	if err != nil {
		return failureFor(err)
	}
	out, err := execution(v)
	if err != nil {
		return nil, err
	}
	return api.GetRuntimeExecution200JSONResponse(out), nil
}

// ListRuntimeEvents возвращает страницу публичных событий и курсор для следующего опроса.
func (h *Handler) ListRuntimeEvents(ctx context.Context, r api.ListRuntimeEventsRequestObject) (api.ListRuntimeEventsResponseObject, error) {
	after := int64(0)
	if r.Params.After != nil {
		after = *r.Params.After
	}
	page, err := h.uc.Events(ctx, owner(ctx), r.Id, after)
	if err != nil {
		return failureFor(err)
	}
	out := api.RuntimeEventsResponse{Events: make([]api.RuntimeEvent, 0, len(page.Events)), NextCursor: page.NextCursor, HasMore: page.HasMore}
	for _, v := range page.Events {
		var body map[string]interface{}
		if err = json.Unmarshal(v.Body, &body); err != nil {
			return nil, err
		}
		out.Events = append(out.Events, api.RuntimeEvent{Seq: v.Seq, Kind: v.Kind, Body: body})
	}
	return api.ListRuntimeEvents200JSONResponse(out), nil
}

// TouchRuntimeSession регистрирует действие пользователя в редакторе; служебный polling эту операцию не
// вызывает.
func (h *Handler) TouchRuntimeSession(ctx context.Context, r api.TouchRuntimeSessionRequestObject) (api.TouchRuntimeSessionResponseObject, error) {
	if err := h.uc.Activity(ctx, owner(ctx), r.Id); err != nil {
		return failureFor(err)
	}
	return api.TouchRuntimeSession200JSONResponse{Accepted: true}, nil
}

// StopRuntimeSession принимает намерение завершить сессию и возвращает 202 до фактического удаления
// ресурсов.
func (h *Handler) StopRuntimeSession(ctx context.Context, r api.StopRuntimeSessionRequestObject) (api.StopRuntimeSessionResponseObject, error) {
	if err := h.uc.Stop(ctx, owner(ctx), r.Id); err != nil {
		return failureFor(err)
	}
	return api.StopRuntimeSession202JSONResponse{Accepted: true}, nil
}

// VisitStartRuntimeSessionResponse записывает безопасную ошибку в HTTP-ответ соответствующей операции.
func (f *failure) VisitStartRuntimeSessionResponse(w http.ResponseWriter) error { return f.write(w) }

// VisitGetActiveRuntimeSessionResponse записывает безопасную ошибку в HTTP-ответ соответствующей операции.
func (f *failure) VisitGetActiveRuntimeSessionResponse(w http.ResponseWriter) error {
	return f.write(w)
}

// VisitGetRuntimeSessionResponse записывает безопасную ошибку в HTTP-ответ соответствующей операции.
func (f *failure) VisitGetRuntimeSessionResponse(w http.ResponseWriter) error { return f.write(w) }

// VisitCreateRuntimeExecutionResponse записывает безопасную ошибку в HTTP-ответ соответствующей операции.
func (f *failure) VisitCreateRuntimeExecutionResponse(w http.ResponseWriter) error { return f.write(w) }

// VisitGetRuntimeExecutionResponse записывает безопасную ошибку в HTTP-ответ соответствующей операции.
func (f *failure) VisitGetRuntimeExecutionResponse(w http.ResponseWriter) error { return f.write(w) }

// VisitListRuntimeEventsResponse записывает безопасную ошибку в HTTP-ответ соответствующей операции.
func (f *failure) VisitListRuntimeEventsResponse(w http.ResponseWriter) error { return f.write(w) }

// VisitTouchRuntimeSessionResponse записывает безопасную ошибку в HTTP-ответ соответствующей операции.
func (f *failure) VisitTouchRuntimeSessionResponse(w http.ResponseWriter) error { return f.write(w) }

// VisitStopRuntimeSessionResponse записывает безопасную ошибку в HTTP-ответ соответствующей операции.
func (f *failure) VisitStopRuntimeSessionResponse(w http.ResponseWriter) error { return f.write(w) }
