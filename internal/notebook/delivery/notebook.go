// Package delivery содержит HTTP-ручки блокнотов.
package delivery

import (
	"context"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/httperr"
)

// Handler отдаёт заглушки ручек блокнотов.
type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
}

func (h *Handler) ListNotebooks(context.Context, api.ListNotebooksRequestObject) (api.ListNotebooksResponseObject, error) {
	return nil, httperr.ErrNotImplemented
}

func (h *Handler) CreateNotebook(context.Context, api.CreateNotebookRequestObject) (api.CreateNotebookResponseObject, error) {
	return nil, httperr.ErrNotImplemented
}

func (h *Handler) GetNotebook(context.Context, api.GetNotebookRequestObject) (api.GetNotebookResponseObject, error) {
	return nil, httperr.ErrNotImplemented
}
