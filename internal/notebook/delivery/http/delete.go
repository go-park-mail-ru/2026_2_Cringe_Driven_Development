package delivery

import (
	"context"
	"errors"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
)

// DeleteNotebook возвращает 204 после удаления или 409, пока ноутбук используется.
func (h *Handler) DeleteNotebook(ctx context.Context, req api.DeleteNotebookRequestObject) (api.DeleteNotebookResponseObject, error) {
	err := h.uc.DeleteNotebook(ctx, req.Id, userID(ctx))
	switch {
	case errors.Is(err, usecase.ErrNotebookNotFound):
		return api.DeleteNotebook404JSONResponse{Code: api.ErrorCodeNotFound, Message: err.Error()}, nil
	case errors.Is(err, usecase.ErrNotebookInUse):
		return api.DeleteNotebook409JSONResponse{Code: api.NotebookInUse, Message: "Нельзя удалить работающий блокнот. Сначала остановите runtime-сессию."}, nil
	case err != nil:
		return nil, err
	}
	return api.DeleteNotebook204Response{}, nil
}
