package delivery

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/middleware"
)

// ImportNotebook проверяет загрузку до вызова операции сохранения.
func (h *Handler) ImportNotebook(ctx context.Context, req api.ImportNotebookRequestObject) (api.ImportNotebookResponseObject, error) {
	name, file, err := readImportedNotebook(req.Body)
	var limit *http.MaxBytesError
	// multipart.Reader может завершиться до эпилога HTTP-тела.
	if body := middleware.NotebookImportBody(ctx); body != nil {
		_, drainErr := io.Copy(io.Discard, body)
		if err == nil || errors.As(drainErr, &limit) {
			err = drainErr
		}
	}
	if err != nil {
		if errors.As(err, &limit) || errors.Is(err, usecase.ErrTooLarge) {
			return importTooLarge(), nil
		}
		return api.ImportNotebook400JSONResponse{Code: api.ErrorCodeValidationError, Message: "expected one name and one nonempty file in a complete multipart body"}, nil
	}
	n, skipped, err := h.uc.Import(ctx, userID(ctx), name, file)
	switch {
	case errors.Is(err, usecase.ErrTooLarge):
		return importTooLarge(), nil
	case errors.Is(err, usecase.ErrInvalidDocument), errors.Is(err, usecase.ErrEmptyName), errors.Is(err, usecase.ErrInvalidName):
		return api.ImportNotebook400JSONResponse{Code: api.ErrorCodeValidationError, Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	return api.ImportNotebook201JSONResponse{Notebook: toAPINotebook(n), SkippedRawCells: skipped}, nil
}

// importTooLarge формирует общий ответ для лимитов файла, документа и тела.
func importTooLarge() api.ImportNotebook413JSONResponse {
	return api.ImportNotebook413JSONResponse{Code: api.ErrorCodeValidationError, Message: "notebook import exceeds size limit"}
}

// readImportedNotebook читает по одной части name и file в произвольном порядке.
func readImportedNotebook(reader *multipart.Reader) (string, []byte, error) {
	invalid := errors.New("invalid notebook multipart")
	if reader == nil {
		return "", nil, invalid
	}
	var name, file []byte
	for {
		part, err := reader.NextRawPart()
		// Обёрнутый EOF означает, что завершающая граница не прочитана.
		if err == io.EOF { //nolint:errorlint // Только прямой EOF завершает multipart.
			break
		}
		if err != nil {
			return "", nil, err
		}
		switch {
		case part.FormName() == "name" && name == nil && part.FileName() == "":
			const maxNameBytes = 128 * 4
			name, err = io.ReadAll(io.LimitReader(part, maxNameBytes+1))
			if len(name) > maxNameBytes {
				return "", nil, invalid
			}
		case part.FormName() == "file" && file == nil && part.FileName() != "":
			file, err = io.ReadAll(io.LimitReader(part, usecase.MaxNotebookBytes+1))
			if len(file) > usecase.MaxNotebookBytes {
				return "", nil, usecase.ErrTooLarge
			}
		default:
			return "", nil, invalid
		}
		if err != nil {
			return "", nil, err
		}
	}
	if name == nil || len(file) == 0 {
		return "", nil, invalid
	}
	return string(name), file, nil
}
