// Package user отвечает за регистрацию, вход, выход и профиль пользователя.
package user

import (
	"context"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/httperr"
)

type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
}

func (h *Handler) RegisterUser(context.Context, api.RegisterUserRequestObject) (api.RegisterUserResponseObject, error) {
	return nil, httperr.ErrNotImplemented
}

func (h *Handler) LoginUser(context.Context, api.LoginUserRequestObject) (api.LoginUserResponseObject, error) {
	return nil, httperr.ErrNotImplemented
}

func (h *Handler) RefreshToken(context.Context, api.RefreshTokenRequestObject) (api.RefreshTokenResponseObject, error) {
	return nil, httperr.ErrNotImplemented
}

func (h *Handler) LogoutUser(context.Context, api.LogoutUserRequestObject) (api.LogoutUserResponseObject, error) {
	return nil, httperr.ErrNotImplemented
}

func (h *Handler) GetCurrentUser(context.Context, api.GetCurrentUserRequestObject) (api.GetCurrentUserResponseObject, error) {
	return nil, httperr.ErrNotImplemented
}
