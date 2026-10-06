// Package delivery содержит HTTP-ручки пользователя. Они переводят ошибки
// usecase в ответы контракта и выдают токены в HttpOnly-cookie.
package delivery

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/auth"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/models"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
)

const refreshCookie = "refresh_token"

// CookieConfig хранит настройки cookie с токенами.
type CookieConfig struct {
	AccessPath  string
	RefreshPath string
	AccessTTL   time.Duration
	// Secure отправляет cookie только по HTTPS. Локально HTTPS нет, поэтому это настройка.
	Secure bool
}

type Handler struct {
	uc     usecase.Usecase
	cookie CookieConfig
}

func NewHandler(uc usecase.Usecase, cookie CookieConfig) *Handler {
	return &Handler{uc: uc, cookie: cookie}
}

func (h *Handler) RegisterUser(ctx context.Context, req api.RegisterUserRequestObject) (api.RegisterUserResponseObject, error) {
	u, sess, err := h.uc.Register(ctx, req.Body.Login, req.Body.Password)
	switch {
	case errors.Is(err, usecase.ErrLoginTaken):
		return api.RegisterUser409JSONResponse{Code: api.LoginTaken, Message: "login is already taken"}, nil
	case errors.Is(err, usecase.ErrPasswordTooLong):
		return api.RegisterUser400JSONResponse{Code: api.ValidationError, Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	cookies := h.sessionCookies(sess)
	return registerResponse{api.RegisterUser201JSONResponse{Body: toAPIUser(u)}, cookies}, nil
}

func (h *Handler) LoginUser(ctx context.Context, req api.LoginUserRequestObject) (api.LoginUserResponseObject, error) {
	u, sess, err := h.uc.Login(ctx, req.Body.Login, req.Body.Password)
	switch {
	case errors.Is(err, usecase.ErrInvalidCredentials):
		return api.LoginUser401JSONResponse{Code: api.InvalidCredentials, Message: "invalid login or password"}, nil
	case err != nil:
		return nil, err
	}
	cookies := h.sessionCookies(sess)
	return loginResponse{api.LoginUser200JSONResponse{Body: toAPIUser(u)}, cookies}, nil
}

func (h *Handler) RefreshToken(ctx context.Context, req api.RefreshTokenRequestObject) (api.RefreshTokenResponseObject, error) {
	sess, err := h.uc.Refresh(ctx, req.Params.RefreshToken)
	switch {
	case errors.Is(err, usecase.ErrInvalidRefresh):
		return api.RefreshToken401JSONResponse{Code: api.Unauthorized, Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	cookies := h.sessionCookies(sess)
	return refreshResponse{api.RefreshToken204Response{}, cookies}, nil
}

func (h *Handler) LogoutUser(ctx context.Context, req api.LogoutUserRequestObject) (api.LogoutUserResponseObject, error) {
	err := h.uc.Logout(ctx, req.Params.RefreshToken)
	switch {
	case errors.Is(err, usecase.ErrInvalidRefresh):
		return api.LogoutUser401JSONResponse{Code: api.Unauthorized, Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	// Отрицательный MaxAge велит браузеру удалить cookie.
	cookies := responseCookies{
		h.tokenCookie("access_token", "", h.cookie.AccessPath, -1),
		h.tokenCookie(refreshCookie, "", h.cookie.RefreshPath, -1),
	}
	return logoutResponse{api.LogoutUser204Response{}, cookies}, nil
}

func (h *Handler) GetCurrentUser(ctx context.Context, _ api.GetCurrentUserRequestObject) (api.GetCurrentUserResponseObject, error) {
	// Без пользователя в контексте сюда не попасть: Validator отвечает 401 раньше.
	id, _ := auth.UserIDFromContext(ctx)
	u, err := h.uc.CurrentUser(ctx, id)
	switch {
	case errors.Is(err, usecase.ErrUserNotFound):
		// Токен ещё жив, а пользователя уже удалили.
		return api.GetCurrentUser401JSONResponse{Code: api.Unauthorized, Message: "user no longer exists"}, nil
	case err != nil:
		return nil, err
	}
	return api.GetCurrentUser200JSONResponse(toAPIUser(u)), nil
}

func (h *Handler) sessionCookies(sess models.Session) responseCookies {
	return responseCookies{
		h.tokenCookie("access_token", sess.AccessToken, h.cookie.AccessPath, int(h.cookie.AccessTTL/time.Second)),
		h.tokenCookie(refreshCookie, sess.RefreshToken, h.cookie.RefreshPath, int(time.Until(sess.ExpiresAt).Seconds())),
	}
}

func (h *Handler) tokenCookie(name, value, path string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: value, Path: path, MaxAge: maxAge,
		HttpOnly: true, Secure: h.cookie.Secure, SameSite: http.SameSiteLaxMode,
	}
}

func toAPIUser(u models.User) api.User {
	return api.User{Id: u.ID, Login: u.Login}
}
