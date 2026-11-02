package delivery

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
)

func (h *Handler) GetNotebookContent(ctx context.Context, req api.GetNotebookContentRequestObject) (api.GetNotebookContentResponseObject, error) {
	result, err := h.uc.Content(ctx, req.Id, userID(ctx))
	if errors.Is(err, usecase.ErrNotebookNotFound) {
		return api.GetNotebookContent404JSONResponse{Code: api.NotebookEditingErrorCodeNotFound, Message: err.Error()}, nil
	}
	if err != nil {
		return nil, err
	}
	cells := make([]api.Cell, 0, len(result.Cells))
	for _, cell := range result.Cells {
		cells = append(cells, toAPICell(cell))
	}
	return api.GetNotebookContent200JSONResponse{Body: api.NotebookContent{Hash: result.Hash, Cells: cells}, Headers: api.GetNotebookContent200ResponseHeaders{ETag: quotedETag(result.Hash)}}, nil
}

// parseIfMatch принимает только один непустой сильный ETag; списки и wildcard
// не позволяют обходить проверку редакции блокнота.
func parseIfMatch(value string) (string, error) {
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", errors.New("If-Match must contain one strong ETag")
	}
	hash := value[1 : len(value)-1]
	if strings.ContainsAny(hash, "\" \t\r\n") {
		return "", errors.New("If-Match must contain one strong ETag")
	}
	for _, c := range []byte(hash) {
		if c < 0x21 || c == 0x7f {
			return "", errors.New("invalid If-Match")
		}
	}
	return hash, nil
}

func quotedETag(hash string) *string { value := `"` + hash + `"`; return &value }

func editingError(err error) (int, api.NotebookEditingError) {
	code := api.NotebookEditingErrorCodeValidationError
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, usecase.ErrNotebookConflict):
		status = http.StatusConflict
		code = api.NotebookEditingErrorCodeNotebookConflict
	case errors.Is(err, usecase.ErrTooManyCells):
		status = http.StatusConflict
		code = api.NotebookEditingErrorCodeTooManyCells
	case errors.Is(err, usecase.ErrNotebookNotFound), errors.Is(err, usecase.ErrCellNotFound):
		status = http.StatusNotFound
		code = api.NotebookEditingErrorCodeNotFound
	case errors.Is(err, usecase.ErrIndexOutOfRange), errors.Is(err, usecase.ErrInvalidCellKind), errors.Is(err, usecase.ErrInvalidHash):
	default:
		return 0, api.NotebookEditingError{}
	}
	return status, api.NotebookEditingError{Code: code, Message: err.Error()}
}

func (h *Handler) AddNotebookCell(ctx context.Context, req api.AddNotebookCellRequestObject) (api.AddNotebookCellResponseObject, error) {
	hash, err := parseIfMatch(req.Params.IfMatch)
	if err != nil {
		return api.AddNotebookCell400JSONResponse{Code: api.NotebookEditingErrorCodeValidationError, Message: err.Error()}, nil
	}
	result, err := h.uc.AddCell(ctx, req.Id, userID(ctx), hash, domain.CellKind(req.Body.Kind), req.Body.Source, req.Body.Index)
	if err != nil {
		status, body := editingError(err)
		switch status {
		case http.StatusBadRequest:
			return api.AddNotebookCell400JSONResponse(body), nil
		case http.StatusNotFound:
			return api.AddNotebookCell404JSONResponse(body), nil
		case http.StatusConflict:
			return api.AddNotebookCell409JSONResponse(body), nil
		default:
			return nil, err
		}
	}
	return api.AddNotebookCell201JSONResponse{Body: api.NotebookCellChange{Hash: result.Hash, Cell: toAPICell(result.Cell)}, Headers: api.AddNotebookCell201ResponseHeaders{ETag: quotedETag(result.Hash)}}, nil
}

func (h *Handler) EditNotebookCell(ctx context.Context, req api.EditNotebookCellRequestObject) (api.EditNotebookCellResponseObject, error) {
	hash, err := parseIfMatch(req.Params.IfMatch)
	if err != nil {
		return api.EditNotebookCell400JSONResponse{Code: api.NotebookEditingErrorCodeValidationError, Message: err.Error()}, nil
	}
	result, err := h.uc.EditCell(ctx, req.Id, userID(ctx), hash, req.CellId, req.Body.Source)
	if err != nil {
		status, body := editingError(err)
		switch status {
		case http.StatusBadRequest:
			return api.EditNotebookCell400JSONResponse(body), nil
		case http.StatusNotFound:
			return api.EditNotebookCell404JSONResponse(body), nil
		case http.StatusConflict:
			return api.EditNotebookCell409JSONResponse(body), nil
		default:
			return nil, err
		}
	}
	return api.EditNotebookCell200JSONResponse{Body: api.NotebookCellChange{Hash: result.Hash, Cell: toAPICell(result.Cell)}, Headers: api.EditNotebookCell200ResponseHeaders{ETag: quotedETag(result.Hash)}}, nil
}

func (h *Handler) MoveNotebookCell(ctx context.Context, req api.MoveNotebookCellRequestObject) (api.MoveNotebookCellResponseObject, error) {
	hash, err := parseIfMatch(req.Params.IfMatch)
	if err != nil {
		return api.MoveNotebookCell400JSONResponse{Code: api.NotebookEditingErrorCodeValidationError, Message: err.Error()}, nil
	}
	result, err := h.uc.MoveCell(ctx, req.Id, userID(ctx), hash, req.CellId, req.Body.Index)
	if err != nil {
		status, body := editingError(err)
		switch status {
		case http.StatusBadRequest:
			return api.MoveNotebookCell400JSONResponse(body), nil
		case http.StatusNotFound:
			return api.MoveNotebookCell404JSONResponse(body), nil
		case http.StatusConflict:
			return api.MoveNotebookCell409JSONResponse(body), nil
		default:
			return nil, err
		}
	}
	return api.MoveNotebookCell200JSONResponse{Body: api.NotebookContentChange{Hash: result}, Headers: api.MoveNotebookCell200ResponseHeaders{ETag: quotedETag(result)}}, nil
}

func (h *Handler) RemoveNotebookCell(ctx context.Context, req api.RemoveNotebookCellRequestObject) (api.RemoveNotebookCellResponseObject, error) {
	hash, err := parseIfMatch(req.Params.IfMatch)
	if err != nil {
		return api.RemoveNotebookCell400JSONResponse{Code: api.NotebookEditingErrorCodeValidationError, Message: err.Error()}, nil
	}
	result, err := h.uc.RemoveCell(ctx, req.Id, userID(ctx), hash, req.CellId)
	if err != nil {
		status, body := editingError(err)
		switch status {
		case http.StatusBadRequest:
			return api.RemoveNotebookCell400JSONResponse(body), nil
		case http.StatusNotFound:
			return api.RemoveNotebookCell404JSONResponse(body), nil
		case http.StatusConflict:
			return api.RemoveNotebookCell409JSONResponse(body), nil
		default:
			return nil, err
		}
	}
	return api.RemoveNotebookCell200JSONResponse{Body: api.NotebookContentChange{Hash: result}, Headers: api.RemoveNotebookCell200ResponseHeaders{ETag: quotedETag(result)}}, nil
}
