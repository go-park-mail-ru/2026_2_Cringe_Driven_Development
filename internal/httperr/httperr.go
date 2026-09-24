// Package httperr writes errors in the Error format of the API contract.
package httperr

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
)

// ErrNotImplemented is returned by handlers that are not written yet; it becomes 501.
var ErrNotImplemented = errors.New("not implemented")

// Write sends an Error with the given status.
func Write(w http.ResponseWriter, status int, code api.ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(api.Error{Code: code, Message: message})
}

// Internal logs err and sends 500 without exposing the details.
func Internal(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "internal error",
		slog.String("path", r.URL.Path),
		slog.String("error", err.Error()),
	)
	Write(w, http.StatusInternalServerError, api.Internal, "internal server error")
}

// RequestError answers requests the generated code could not parse.
func RequestError(w http.ResponseWriter, _ *http.Request, err error) {
	var required *api.RequiredParamError
	if errors.As(err, &required) && required.ParamName == "refresh_token" {
		// Without the refresh_token cookie the user is simply not logged in.
		Write(w, http.StatusUnauthorized, api.Unauthorized, "refresh token is missing")
		return
	}
	Write(w, http.StatusBadRequest, api.ValidationError, err.Error())
}

// ResponseError answers errors returned by handlers.
func ResponseError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrNotImplemented) {
		Write(w, http.StatusNotImplemented, api.NotImplemented, "not implemented yet")
		return
	}
	Internal(w, r, err)
}

// NotFound answers requests to unknown paths.
func NotFound(w http.ResponseWriter, _ *http.Request) {
	Write(w, http.StatusNotFound, api.NotFound, "no such endpoint")
}

// MethodNotAllowed answers requests with a method the path does not support.
func MethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	Write(w, http.StatusMethodNotAllowed, api.ValidationError, "method not allowed")
}
