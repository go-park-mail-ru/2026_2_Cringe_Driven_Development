// Package user registers users, logs them in and out and serves their profile.
package user

import (
	"context"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/httperr"
)

// Handler implements the auth and users operations of the contract.
type Handler struct{}

// NewHandler creates the handler.
func NewHandler() *Handler {
	return &Handler{}
}

// RegisterUser creates a user.
func (h *Handler) RegisterUser(context.Context, api.RegisterUserRequestObject) (api.RegisterUserResponseObject, error) {
	return nil, httperr.ErrNotImplemented
}

// LoginUser checks the credentials and starts a session.
func (h *Handler) LoginUser(context.Context, api.LoginUserRequestObject) (api.LoginUserResponseObject, error) {
	return nil, httperr.ErrNotImplemented
}

// RefreshToken replaces the refresh session and issues a new access token.
func (h *Handler) RefreshToken(context.Context, api.RefreshTokenRequestObject) (api.RefreshTokenResponseObject, error) {
	return nil, httperr.ErrNotImplemented
}

// LogoutUser ends the refresh session.
func (h *Handler) LogoutUser(context.Context, api.LogoutUserRequestObject) (api.LogoutUserResponseObject, error) {
	return nil, httperr.ErrNotImplemented
}

// GetCurrentUser returns the authenticated user.
func (h *Handler) GetCurrentUser(context.Context, api.GetCurrentUserRequestObject) (api.GetCurrentUserResponseObject, error) {
	return nil, httperr.ErrNotImplemented
}
