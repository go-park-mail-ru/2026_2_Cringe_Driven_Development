// Package httperr отдаёт ошибки в формате Error из контракта.
package httperr

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
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
	slog.ErrorContext(r.Context(), "internal error",
		slog.String("path", r.URL.Path),
		slog.String("error", err.Error()),
	)
	Write(w, http.StatusInternalServerError, api.Internal, "internal server error")
}

// RequestError отвечает, когда сгенерированный код не смог разобрать запрос.
func RequestError(w http.ResponseWriter, _ *http.Request, err error) {
	var required *api.RequiredParamError
	if errors.As(err, &required) && required.ParamName == "refresh_token" {
		// Нет cookie refresh_token — значит, пользователь не вошёл.
		Write(w, http.StatusUnauthorized, api.Unauthorized, "refresh token is missing")
		return
	}
	Write(w, http.StatusBadRequest, api.ValidationError, err.Error())
}

// ResponseError отвечает на ошибки, которые вернули хендлеры.
func ResponseError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrNotImplemented) {
		Write(w, http.StatusNotImplemented, api.NotImplemented, "not implemented yet")
		return
	}
	Internal(w, r, err)
}

func NotFound(w http.ResponseWriter, _ *http.Request) {
	Write(w, http.StatusNotFound, api.NotFound, "no such endpoint")
}

func MethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	Write(w, http.StatusMethodNotAllowed, api.ValidationError, "method not allowed")
}
