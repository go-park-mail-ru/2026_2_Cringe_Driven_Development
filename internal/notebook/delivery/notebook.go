// Package delivery содержит HTTP-ручки блокнотов.
package delivery

import (
	"context"
	"errors"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/auth"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/models"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
)

// Handler реализует ручки блокнотов из api.StrictServerInterface.
type Handler struct {
	uc usecase.Usecase
}

// NewHandler принимает usecase блокнотов.
func NewHandler(uc usecase.Usecase) *Handler {
	return &Handler{uc: uc}
}

// ListNotebooks отвечает 200 со списком, без блокнотов — [].
func (h *Handler) ListNotebooks(ctx context.Context, _ api.ListNotebooksRequestObject) (api.ListNotebooksResponseObject, error) {
	list, err := h.uc.List(ctx, userID(ctx))
	if err != nil {
		return nil, err
	}
	resp := make(api.ListNotebooks200JSONResponse, 0, len(list))
	for _, n := range list {
		resp = append(resp, api.NotebookSummary{
			Id:         n.ID,
			Name:       n.Name,
			CellsCount: n.CellsCount,
			UpdatedAt:  n.UpdatedAt,
		})
	}
	return resp, nil
}

// CreateNotebook отвечает 201 с новым блокнотом или 400 на пустое имя.
func (h *Handler) CreateNotebook(ctx context.Context, req api.CreateNotebookRequestObject) (api.CreateNotebookResponseObject, error) {
	n, err := h.uc.Create(ctx, userID(ctx), req.Body.Name)
	switch {
	case errors.Is(err, usecase.ErrEmptyName):
		return api.CreateNotebook400JSONResponse{Code: api.ValidationError, Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	return api.CreateNotebook201JSONResponse(toAPINotebook(n)), nil
}

// GetNotebook отвечает 200 с блоками или 404 на чужой блокнот.
func (h *Handler) GetNotebook(ctx context.Context, req api.GetNotebookRequestObject) (api.GetNotebookResponseObject, error) {
	n, err := h.uc.Get(ctx, req.Id, userID(ctx))
	switch {
	case errors.Is(err, usecase.ErrNotebookNotFound):
		return api.GetNotebook404JSONResponse{Code: api.NotFound, Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	return api.GetNotebook200JSONResponse(toAPINotebook(n)), nil
}

// CreateCell отвечает 201 с новым блоком; 409 означает предел блоков.
func (h *Handler) CreateCell(ctx context.Context, req api.CreateCellRequestObject) (api.CreateCellResponseObject, error) {
	cell, err := h.uc.CreateCell(ctx, req.Id, userID(ctx), models.CellKind(req.Body.Kind), req.Body.Index)
	switch {
	case errors.Is(err, usecase.ErrNotebookNotFound):
		return api.CreateCell404JSONResponse{Code: api.NotFound, Message: err.Error()}, nil
	case errors.Is(err, usecase.ErrIndexOutOfRange):
		return api.CreateCell400JSONResponse{Code: api.ValidationError, Message: err.Error()}, nil
	case errors.Is(err, usecase.ErrTooManyCells):
		return api.CreateCell409JSONResponse{Code: api.ValidationError, Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	return api.CreateCell201JSONResponse(toAPICell(cell)), nil
}

// DeleteCell отвечает 204 или 404, если нет блокнота или блока.
func (h *Handler) DeleteCell(ctx context.Context, req api.DeleteCellRequestObject) (api.DeleteCellResponseObject, error) {
	err := h.uc.DeleteCell(ctx, req.Id, userID(ctx), req.Index)
	switch {
	case errors.Is(err, usecase.ErrNotebookNotFound), errors.Is(err, usecase.ErrCellNotFound):
		return api.DeleteCell404JSONResponse{Code: api.NotFound, Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	return api.DeleteCell204Response{}, nil
}

// userID берёт пользователя из контекста: без него Validator уже ответил 401.
func userID(ctx context.Context) int64 {
	id, _ := auth.UserIDFromContext(ctx)
	return id
}

func toAPINotebook(n models.Notebook) api.Notebook {
	cells := make([]api.Cell, 0, len(n.Cells))
	for _, c := range n.Cells {
		cells = append(cells, toAPICell(c))
	}
	return api.Notebook{
		Id:        n.ID,
		Name:      n.Name,
		Cells:     cells,
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
	}
}

func toAPICell(c models.Cell) api.Cell {
	return api.Cell{Id: c.ID, Kind: api.CellKind(c.Kind), Source: c.Source}
}
