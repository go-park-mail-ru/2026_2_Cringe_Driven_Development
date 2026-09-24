// Package user отвечает за регистрацию, вход, выход и профиль пользователя.
package user

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/auth"
)

const refreshCookie = "refresh_token"

// CookieConfig — настройки cookie с refresh-токеном.
type CookieConfig struct {
	// Path — чтобы браузер отправлял cookie только в ручки /auth.
	Path string
	// Secure — только по HTTPS. Локально HTTPS нет, поэтому это настройка.
	Secure bool
}

type Handler struct {
	svc    *Service
	cookie CookieConfig
}

func NewHandler(svc *Service, cookie CookieConfig) *Handler {
	return &Handler{svc: svc, cookie: cookie}
}

func (h *Handler) RegisterUser(ctx context.Context, req api.RegisterUserRequestObject) (api.RegisterUserResponseObject, error) {
	u, sess, err := h.svc.Register(ctx, req.Body.Login, req.Body.Password)
	switch {
	case errors.Is(err, ErrLoginTaken):
		return api.RegisterUser409JSONResponse{Code: api.LoginTaken, Message: "login is already taken"}, nil
	case errors.Is(err, ErrPasswordTooLong):
		return api.RegisterUser400JSONResponse{Code: api.ValidationError, Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	bearer, cookie := h.sessionHeaders(sess)
	return api.RegisterUser201JSONResponse{
		Body:    toAPIUser(u),
		Headers: api.RegisterUser201ResponseHeaders{Authorization: &bearer, SetCookie: &cookie},
	}, nil
}

func (h *Handler) LoginUser(ctx context.Context, req api.LoginUserRequestObject) (api.LoginUserResponseObject, error) {
	u, sess, err := h.svc.Login(ctx, req.Body.Login, req.Body.Password)
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		return api.LoginUser401JSONResponse{Code: api.InvalidCredentials, Message: "invalid login or password"}, nil
	case err != nil:
		return nil, err
	}
	bearer, cookie := h.sessionHeaders(sess)
	return api.LoginUser200JSONResponse{
		Body:    toAPIUser(u),
		Headers: api.LoginUser200ResponseHeaders{Authorization: &bearer, SetCookie: &cookie},
	}, nil
}

func (h *Handler) RefreshToken(ctx context.Context, req api.RefreshTokenRequestObject) (api.RefreshTokenResponseObject, error) {
	sess, err := h.svc.Refresh(ctx, req.Params.RefreshToken)
	switch {
	case errors.Is(err, ErrInvalidRefresh):
		return api.RefreshToken401JSONResponse{Code: api.Unauthorized, Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	bearer, cookie := h.sessionHeaders(sess)
	return api.RefreshToken204Response{
		Headers: api.RefreshToken204ResponseHeaders{Authorization: &bearer, SetCookie: &cookie},
	}, nil
}

func (h *Handler) LogoutUser(ctx context.Context, req api.LogoutUserRequestObject) (api.LogoutUserResponseObject, error) {
	err := h.svc.Logout(ctx, req.Params.RefreshToken)
	switch {
	case errors.Is(err, ErrInvalidRefresh):
		return api.LogoutUser401JSONResponse{Code: api.Unauthorized, Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	// Отрицательный MaxAge велит браузеру удалить cookie.
	cookie := h.refreshCookie("", -1).String()
	return api.LogoutUser204Response{Headers: api.LogoutUser204ResponseHeaders{SetCookie: &cookie}}, nil
}

func (h *Handler) GetCurrentUser(ctx context.Context, _ api.GetCurrentUserRequestObject) (api.GetCurrentUserResponseObject, error) {
	// Без пользователя в контексте сюда не попасть: Validator отвечает 401 раньше.
	id, _ := auth.UserIDFromContext(ctx)
	u, err := h.svc.CurrentUser(ctx, id)
	switch {
	case errors.Is(err, ErrUserNotFound):
		// Токен ещё жив, а пользователя уже удалили.
		return api.GetCurrentUser401JSONResponse{Code: api.Unauthorized, Message: "user no longer exists"}, nil
	case err != nil:
		return nil, err
	}
	return api.GetCurrentUser200JSONResponse(toAPIUser(u)), nil
}

func (h *Handler) sessionHeaders(sess Session) (bearer, cookie string) {
	maxAge := int(time.Until(sess.ExpiresAt).Seconds())
	return "Bearer " + sess.AccessToken, h.refreshCookie(sess.RefreshToken, maxAge).String()
}

func (h *Handler) refreshCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     refreshCookie,
		Value:    value,
		Path:     h.cookie.Path,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   h.cookie.Secure,
		SameSite: http.SameSiteLaxMode,
	}
}

func toAPIUser(u User) api.User {
	return api.User{Id: u.ID, Login: u.Login}
}
