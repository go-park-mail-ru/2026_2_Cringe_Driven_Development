// Package httperr отдаёт ошибки в формате Error из контракта.
package httperr

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	notebookdomain "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/observability"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
)

// ErrNotImplemented возвращают ещё не написанные хендлеры, клиент получает 501.
var ErrNotImplemented = errors.New("not implemented")

func Write(w http.ResponseWriter, status int, code api.ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(api.Error{Code: code, Message: message})
}

// Internal пишет причину в лог, а клиенту отдаёт 500 без подробностей.
func Internal(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "http.internal_error",
		slog.String("error_code", observability.ErrorCode(err)),
	)
	Write(w, http.StatusInternalServerError, api.ErrorCodeInternal, "internal server error")
}

// RequestError отвечает, когда сгенерированный код не смог разобрать запрос.
func RequestError(w http.ResponseWriter, _ *http.Request, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		Write(w, http.StatusRequestEntityTooLarge, api.ErrorCodeValidationError, "request body too large")
		return
	}
	Write(w, http.StatusBadRequest, api.ErrorCodeValidationError, err.Error())
}

// ResponseError отвечает на ошибки, которые вернули хендлеры.
func ResponseError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, notebookdomain.ErrTooLarge) {
		Write(w, http.StatusRequestEntityTooLarge, api.ErrorCodeValidationError, "notebook size limit exceeded")
		return
	}
	if errors.Is(err, ErrNotImplemented) {
		Write(w, http.StatusNotImplemented, api.ErrorCodeNotImplemented, "not implemented yet")
		return
	}
	Internal(w, r, err)
}

func NotFound(w http.ResponseWriter, _ *http.Request) {
	Write(w, http.StatusNotFound, api.ErrorCodeNotFound, "no such endpoint")
}

func MethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	Write(w, http.StatusMethodNotAllowed, api.ErrorCodeValidationError, "method not allowed")
}
