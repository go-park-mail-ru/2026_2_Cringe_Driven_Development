package delivery

import (
	"context"
	"errors"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
)

// RenameNotebook возвращает полный блокнот после изменения имени владельцем.
func (h *Handler) RenameNotebook(ctx context.Context, req api.RenameNotebookRequestObject) (api.RenameNotebookResponseObject, error) {
	n, err := h.uc.Rename(ctx, req.Id, userID(ctx), req.Body.Name)
	switch {
	case errors.Is(err, usecase.ErrEmptyName):
		return api.RenameNotebook400JSONResponse{Code: api.ErrorCodeValidationError, Message: err.Error()}, nil
	case errors.Is(err, usecase.ErrNotebookNotFound):
		return api.RenameNotebook404JSONResponse{Code: api.ErrorCodeNotFound, Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	return api.RenameNotebook200JSONResponse(toAPINotebook(n)), nil
}
