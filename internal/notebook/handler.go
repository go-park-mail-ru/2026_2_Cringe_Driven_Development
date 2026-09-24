// Package notebook serves the notebooks of the authenticated user.
package notebook

import (
	"context"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/httperr"
)

// Handler implements the notebooks operations of the contract.
// The methods are stubs until the notebooks task replaces them.
type Handler struct{}

// NewHandler creates the handler.
func NewHandler() *Handler {
	return &Handler{}
}

// ListNotebooks returns the notebooks of the user.
func (h *Handler) ListNotebooks(context.Context, api.ListNotebooksRequestObject) (api.ListNotebooksResponseObject, error) {
	return nil, httperr.ErrNotImplemented
}

// CreateNotebook creates an empty notebook.
func (h *Handler) CreateNotebook(context.Context, api.CreateNotebookRequestObject) (api.CreateNotebookResponseObject, error) {
	return nil, httperr.ErrNotImplemented
}

// GetNotebook returns one notebook of the user.
func (h *Handler) GetNotebook(context.Context, api.GetNotebookRequestObject) (api.GetNotebookResponseObject, error) {
	return nil, httperr.ErrNotImplemented
}
