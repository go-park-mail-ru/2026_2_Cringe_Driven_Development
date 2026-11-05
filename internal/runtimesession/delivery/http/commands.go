package delivery

import (
	"context"
	"encoding/json"
	"net/http"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	"github.com/google/uuid"
)

// convertPublic переносит публичные данные в сгенерированную модель API через её JSON-представление.
func convertPublic(v, out any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// GetRuntimeQueue возвращает текущую очередь ячеек сессии из сохранённого состояния backend.
func (h *Handler) GetRuntimeQueue(ctx context.Context, r api.GetRuntimeQueueRequestObject) (api.GetRuntimeQueueResponseObject, error) {
	v, err := h.uc.Queue(ctx, owner(ctx), r.Id)
	if err != nil {
		return failureFor(err)
	}
	var out api.RuntimeQueueSnapshot
	err = convertPublic(v, &out)
	if err != nil {
		return nil, err
	}
	return api.GetRuntimeQueue200JSONResponse(out), nil
}

// CreateRuntimeExecutionBatch принимает фиксированный пакет ячеек и возвращает все execution_ids после
// атомарного сохранения.
func (h *Handler) CreateRuntimeExecutionBatch(ctx context.Context, r api.CreateRuntimeExecutionBatchRequestObject) (api.CreateRuntimeExecutionBatchResponseObject, error) {
	if r.Body == nil {
		return failureFor(D.ErrInvalid)
	}
	req := D.BatchRequest{RequestID: r.Body.RequestId, TimeoutSeconds: 300, Cells: make([]D.BatchCell, 0, len(r.Body.Cells))}
	if r.Body.TimeoutSeconds != nil {
		req.TimeoutSeconds = float64(*r.Body.TimeoutSeconds)
		if req.TimeoutSeconds <= 0 {
			return failureFor(D.ErrInvalid)
		}
	}
	for _, c := range r.Body.Cells {
		req.Cells = append(req.Cells, D.BatchCell{CellID: c.CellId, SourceHash: c.SourceHash})
	}
	v, err := h.uc.ExecuteBatch(ctx, owner(ctx), r.Id, req)
	if err != nil {
		return failureFor(err)
	}
	return api.CreateRuntimeExecutionBatch202JSONResponse{RequestId: v.RequestID, SessionId: v.SessionID, ExecutionIds: v.ExecutionIDs}, nil
}

// InspectRuntimeVariables ставит инспекцию переменных в очередь; ответ 202 ещё не означает готовый список
// переменных.
func (h *Handler) InspectRuntimeVariables(ctx context.Context, r api.InspectRuntimeVariablesRequestObject) (api.InspectRuntimeVariablesResponseObject, error) {
	if r.Body == nil {
		return failureFor(D.ErrInvalid)
	}
	v, err := h.uc.Inspect(ctx, owner(ctx), r.Id, D.InspectRequest{ExecutionID: r.Body.ExecutionId})
	if err != nil {
		return failureFor(err)
	}
	var out api.RuntimeInspection
	err = convertPublic(v, &out)
	if err != nil {
		return nil, err
	}
	return api.InspectRuntimeVariables202JSONResponse(out), nil
}

// GetRuntimeInspection возвращает сохранённые состояние и результат инспекции без повторного обращения к
// kernel.
func (h *Handler) GetRuntimeInspection(ctx context.Context, r api.GetRuntimeInspectionRequestObject) (api.GetRuntimeInspectionResponseObject, error) {
	v, err := h.uc.GetInspection(ctx, owner(ctx), r.Id, r.ExecutionId)
	if err != nil {
		return failureFor(err)
	}
	var out api.RuntimeInspection
	err = convertPublic(v, &out)
	if err != nil {
		return nil, err
	}
	return api.GetRuntimeInspection200JSONResponse(out), nil
}

// InterruptRuntimeKernel подтверждает запрос прерывания; окончательное состояние команды читается отдельно.
func (h *Handler) InterruptRuntimeKernel(ctx context.Context, r api.InterruptRuntimeKernelRequestObject) (api.InterruptRuntimeKernelResponseObject, error) {
	if _, err := h.uc.Kernel(ctx, owner(ctx), r.Id, false); err != nil {
		return failureFor(err)
	}
	return api.InterruptRuntimeKernel200JSONResponse{InterruptRequested: true}, nil
}

// RestartRuntimeKernel перезапускает свободный kernel и возвращает проверенный UUID его нового поколения.
func (h *Handler) RestartRuntimeKernel(ctx context.Context, r api.RestartRuntimeKernelRequestObject) (api.RestartRuntimeKernelResponseObject, error) {
	generation, err := h.uc.Kernel(ctx, owner(ctx), r.Id, true)
	if err != nil {
		return failureFor(err)
	}
	id, err := uuid.Parse(generation)
	if err != nil {
		return failureFor(D.ErrUnavailable)
	}
	return api.RestartRuntimeKernel200JSONResponse{Generation: id}, nil
}

// VisitGetRuntimeQueueResponse записывает безопасную ошибку в HTTP-ответ соответствующей операции.
func (f *failure) VisitGetRuntimeQueueResponse(w http.ResponseWriter) error { return f.write(w) }

// VisitCreateRuntimeExecutionBatchResponse записывает безопасную ошибку в HTTP-ответ соответствующей
// операции.
func (f *failure) VisitCreateRuntimeExecutionBatchResponse(w http.ResponseWriter) error {
	return f.write(w)
}

// VisitInspectRuntimeVariablesResponse записывает безопасную ошибку в HTTP-ответ соответствующей операции.
func (f *failure) VisitInspectRuntimeVariablesResponse(w http.ResponseWriter) error {
	return f.write(w)
}

// VisitGetRuntimeInspectionResponse записывает безопасную ошибку в HTTP-ответ соответствующей операции.
func (f *failure) VisitGetRuntimeInspectionResponse(w http.ResponseWriter) error { return f.write(w) }

// VisitInterruptRuntimeKernelResponse записывает безопасную ошибку в HTTP-ответ соответствующей операции.
func (f *failure) VisitInterruptRuntimeKernelResponse(w http.ResponseWriter) error { return f.write(w) }

// VisitRestartRuntimeKernelResponse записывает безопасную ошибку в HTTP-ответ соответствующей операции.
func (f *failure) VisitRestartRuntimeKernelResponse(w http.ResponseWriter) error { return f.write(w) }
