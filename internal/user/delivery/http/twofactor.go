package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/principal"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
)

// EnableTwoFactorAuth returns setup data without activating the secret.
func (h *Handler) EnableTwoFactorAuth(ctx context.Context, _ api.EnableTwoFactorAuthRequestObject) (api.EnableTwoFactorAuthResponseObject, error) {
	id, ok := principal.UserIDFromContext(ctx)
	if !ok {
		return twoFactorFailure{status: 401, body: api.Error{Code: api.ErrorCodeUnauthorized, Message: "authentication required"}}, nil
	}
	setup, err := h.uc.EnableTwoFactor(ctx, id)
	if err != nil {
		failure := mapTwoFactorError(err)
		if failure != nil {
			return *failure, nil
		}
		return nil, err
	}
	return api.EnableTwoFactorAuth200JSONResponse{Body: api.TwoFactorSetupResponse{QrCode: setup.QRCode, Secret: setup.Secret}, Headers: api.EnableTwoFactorAuth200ResponseHeaders{CacheControl: "no-store"}}, nil
}

// ConfirmTwoFactorAuth enables 2FA only after a valid code is consumed.
func (h *Handler) ConfirmTwoFactorAuth(ctx context.Context, req api.ConfirmTwoFactorAuthRequestObject) (api.ConfirmTwoFactorAuthResponseObject, error) {
	id, ok := principal.UserIDFromContext(ctx)
	if !ok {
		return twoFactorFailure{status: 401, body: api.Error{Code: api.ErrorCodeUnauthorized, Message: "authentication required"}}, nil
	}
	if req.Body == nil {
		return *mapTwoFactorError(usecase.ErrValidation), nil
	}
	err := h.uc.ConfirmTwoFactor(ctx, id, req.Body.UserCode)
	if err != nil {
		failure := mapTwoFactorError(err)
		if failure != nil {
			return *failure, nil
		}
		return nil, err
	}
	return api.ConfirmTwoFactorAuth200Response{}, nil
}

// DisableTwoFactorAuth checks the account password before deleting its secret.
func (h *Handler) DisableTwoFactorAuth(ctx context.Context, req api.DisableTwoFactorAuthRequestObject) (api.DisableTwoFactorAuthResponseObject, error) {
	id, ok := principal.UserIDFromContext(ctx)
	if !ok {
		return twoFactorFailure{status: 401, body: api.Error{Code: api.ErrorCodeUnauthorized, Message: "authentication required"}}, nil
	}
	if req.Body == nil || req.Body.Password == nil {
		return *mapTwoFactorError(usecase.ErrValidation), nil
	}
	err := h.uc.DisableTwoFactor(ctx, id, *req.Body.Password)
	if err != nil {
		failure := mapTwoFactorError(err)
		if failure != nil {
			return *failure, nil
		}
		return nil, err
	}
	return api.DisableTwoFactorAuth200Response{}, nil
}

type twoFactorFailure struct {
	status int
	body   api.Error
}

func mapTwoFactorError(err error) *twoFactorFailure {
	f := &twoFactorFailure{}
	switch {
	case errors.Is(err, usecase.ErrValidation):
		f.status = 400
		f.body = api.Error{Code: api.ErrorCodeValidationError, Message: "invalid 2FA state or request"}
	case errors.Is(err, usecase.ErrInvalidTOTP):
		f.status = 401
		f.body = api.Error{Code: api.ErrorCodeInvalidTotp, Message: "invalid or already used TOTP code"}
	case errors.Is(err, usecase.ErrUserNotFound):
		f.status = 401
		f.body = api.Error{Code: api.ErrorCodeUnauthorized, Message: "authentication required"}
	case errors.Is(err, usecase.ErrInvalidCurrentPassword):
		f.status = 403
		f.body = api.Error{Code: api.ErrorCodeInvalidCurrentPassword, Message: "invalid current password"}
	case errors.Is(err, usecase.ErrRateLimited):
		f.status = 429
		f.body = api.Error{Code: api.ErrorCodeRateLimited, Message: "too many requests"}
	default:
		return nil
	}
	return f
}
func (f twoFactorFailure) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.status)
	return json.NewEncoder(w).Encode(f.body)
}
func (f twoFactorFailure) VisitEnableTwoFactorAuthResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f twoFactorFailure) VisitConfirmTwoFactorAuthResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f twoFactorFailure) VisitDisableTwoFactorAuthResponse(w http.ResponseWriter) error {
	return f.write(w)
}
