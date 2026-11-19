package delivery

import (
	"context"
	"errors"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
)

func noStore() *string { value := "no-store"; return &value }

func toAPISharing(settings domain.SharingSettings) api.NotebookSharing {
	readers := make([]api.UserSuggestion, 0, len(settings.Readers))
	for _, reader := range settings.Readers {
		readers = append(readers, api.UserSuggestion{Id: reader.ID, Login: reader.Login})
	}
	return api.NotebookSharing{Mode: api.SharingMode(settings.Mode), Readers: readers}
}

func (h *Handler) GetNotebookSharing(ctx context.Context, req api.GetNotebookSharingRequestObject) (api.GetNotebookSharingResponseObject, error) {
	settings, err := h.uc.GetSharing(ctx, req.Id, userID(ctx))
	if errors.Is(err, usecase.ErrNotebookNotFound) {
		return api.GetNotebookSharing404JSONResponse{Code: api.ErrorCodeNotFound, Message: err.Error()}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.GetNotebookSharing200JSONResponse{Body: toAPISharing(settings), Headers: api.GetNotebookSharing200ResponseHeaders{CacheControl: noStore()}}, nil
}

func (h *Handler) PutNotebookSharing(ctx context.Context, req api.PutNotebookSharingRequestObject) (api.PutNotebookSharingResponseObject, error) {
	if req.Body == nil {
		return api.PutNotebookSharing400JSONResponse{Code: api.ErrorCodeValidationError, Message: "sharing settings are required"}, nil
	}
	settings, err := h.uc.PutSharing(ctx, req.Id, userID(ctx), domain.SharingMode(req.Body.Mode), req.Body.ReaderLogins)
	switch {
	case errors.Is(err, usecase.ErrNotebookNotFound):
		return api.PutNotebookSharing404JSONResponse{Code: api.ErrorCodeNotFound, Message: err.Error()}, nil
	case errors.Is(err, usecase.ErrInvalidSharing):
		return api.PutNotebookSharing400JSONResponse{Code: api.ErrorCodeValidationError, Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	return api.PutNotebookSharing200JSONResponse{Body: toAPISharing(settings), Headers: api.PutNotebookSharing200ResponseHeaders{CacheControl: noStore()}}, nil
}

func (h *Handler) CloneNotebook(ctx context.Context, req api.CloneNotebookRequestObject) (api.CloneNotebookResponseObject, error) {
	if req.Body == nil {
		return api.CloneNotebook400JSONResponse{Code: api.ErrorCodeValidationError, Message: "name is required"}, nil
	}
	n, err := h.uc.Clone(ctx, req.Id, userID(ctx), req.Body.Name)
	switch {
	case errors.Is(err, usecase.ErrNotebookNotFound):
		return api.CloneNotebook404JSONResponse{Code: api.ErrorCodeNotFound, Message: err.Error()}, nil
	case errors.Is(err, usecase.ErrEmptyName), errors.Is(err, usecase.ErrInvalidName):
		return api.CloneNotebook400JSONResponse{Code: api.ErrorCodeValidationError, Message: err.Error()}, nil
	case errors.Is(err, usecase.ErrTooLarge), errors.Is(err, usecase.ErrTooManyCells):
		return api.CloneNotebook413JSONResponse{Code: api.ErrorCodeValidationError, Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	return api.CloneNotebook201JSONResponse(toAPINotebook(n)), nil
}
