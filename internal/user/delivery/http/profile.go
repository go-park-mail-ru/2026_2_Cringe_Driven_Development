package delivery

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/middleware"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/principal"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
)

// AvatarUploader persists a sanitized upload through the application layer.
type AvatarUploader interface {
	Upload(context.Context, int64, io.ReadSeeker, string) (domain.User, error)
}

type Option func(*Handler)

func WithProfile(avatars AvatarUploader, publicURL string) Option {
	return func(h *Handler) { h.avatars = avatars; h.avatarURL = strings.TrimRight(publicURL, "/") }
}

func (h *Handler) UpdateCurrentUser(ctx context.Context, req api.UpdateCurrentUserRequestObject) (api.UpdateCurrentUserResponseObject, error) {
	id, ok := principal.UserIDFromContext(ctx)
	if !ok {
		return api.UpdateCurrentUser401JSONResponse{Code: api.UserProfileErrorCodeUnauthorized, Message: "authentication required"}, nil
	}
	if req.Body == nil {
		return api.UpdateCurrentUser400JSONResponse{Code: api.UserProfileErrorCodeValidationError, Message: "invalid request body"}, nil
	}
	user, err := h.uc.UpdateCurrentUser(ctx, id, req.Body.Login)
	switch {
	case errors.Is(err, usecase.ErrLoginTaken):
		return api.UpdateCurrentUser409JSONResponse{Code: api.UserProfileErrorCodeLoginTaken, Message: "login is already taken"}, nil
	case errors.Is(err, usecase.ErrValidation):
		return api.UpdateCurrentUser400JSONResponse{Code: api.UserProfileErrorCodeValidationError, Message: "invalid login"}, nil
	case errors.Is(err, usecase.ErrUserNotFound):
		return api.UpdateCurrentUser401JSONResponse{Code: api.UserProfileErrorCodeUnauthorized, Message: "user no longer exists"}, nil
	case err != nil:
		return nil, err
	default:
		return api.UpdateCurrentUser200JSONResponse(h.toAPIUser(user)), nil
	}
}

func (h *Handler) ChangeCurrentUserPassword(ctx context.Context, req api.ChangeCurrentUserPasswordRequestObject) (api.ChangeCurrentUserPasswordResponseObject, error) {
	id, ok := principal.UserIDFromContext(ctx)
	if !ok {
		return api.ChangeCurrentUserPassword401JSONResponse{Code: api.UserProfileErrorCodeUnauthorized, Message: "authentication required"}, nil
	}
	if req.Body == nil || req.Body.CurrentPassword == nil || req.Body.NewPassword == nil {
		return api.ChangeCurrentUserPassword400JSONResponse{Code: api.UserProfileErrorCodeValidationError, Message: "invalid request body"}, nil
	}
	err := h.uc.ChangeCurrentUserPassword(ctx, id, *req.Body.CurrentPassword, *req.Body.NewPassword)
	switch {
	case errors.Is(err, usecase.ErrInvalidCurrentPassword):
		return api.ChangeCurrentUserPassword400JSONResponse{Code: api.UserProfileErrorCodeInvalidCurrentPassword, Message: "current password is incorrect"}, nil
	case errors.Is(err, usecase.ErrValidation):
		return api.ChangeCurrentUserPassword400JSONResponse{Code: api.UserProfileErrorCodeValidationError, Message: "invalid password or passwords match"}, nil
	case errors.Is(err, usecase.ErrUserNotFound):
		return api.ChangeCurrentUserPassword401JSONResponse{Code: api.UserProfileErrorCodeUnauthorized, Message: "user no longer exists"}, nil
	case err != nil:
		return nil, err
	default:
		return api.ChangeCurrentUserPassword204Response{}, nil
	}
}

func (h *Handler) UploadCurrentUserAvatar(ctx context.Context, req api.UploadCurrentUserAvatarRequestObject) (api.UploadCurrentUserAvatarResponseObject, error) {
	id, ok := principal.UserIDFromContext(ctx)
	if !ok {
		return avatarError(usecase.ErrUserNotFound)
	}
	if req.Body == nil {
		return avatarError(usecase.ErrInvalidImage)
	}
	part, err := req.Body.NextRawPart()
	if err != nil {
		return avatarParseError(err)
	}
	defer func() { _ = part.Close() }()
	if part.FormName() != "avatar" || part.FileName() == "" {
		return avatarError(usecase.ErrInvalidImage)
	}
	contentType, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
	if err != nil || (contentType != "image/jpeg" && contentType != "image/png") {
		return avatarError(usecase.ErrUnsupportedMediaType)
	}
	source, err := os.CreateTemp("", "avatar-source-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = source.Close(); _ = os.Remove(source.Name()) }()
	n, err := io.Copy(source, io.LimitReader(part, 5*1024*1024+1))
	if err != nil {
		return avatarError(err)
	}
	if n > 5*1024*1024 {
		return avatarError(usecase.ErrAvatarTooLarge)
	}
	if n == 0 {
		return avatarError(usecase.ErrInvalidImage)
	}
	// Validate every part before publishing; never ignore a second file/field.
	if _, err = req.Body.NextRawPart(); !errors.Is(err, io.EOF) {
		return avatarParseError(err)
	}
	// multipart.Reader can stop before the epilogue. Count the entire HTTP body.
	if body := middleware.AvatarRequestBody(ctx); body != nil {
		if _, err = io.Copy(io.Discard, body); err != nil {
			return avatarError(err)
		}
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if h.avatars == nil {
		return nil, errors.New("avatar uploader is not configured")
	}
	user, err := h.avatars.Upload(ctx, id, source, contentType)
	if err != nil {
		return avatarError(err)
	}
	return api.UploadCurrentUserAvatar200JSONResponse(h.toAPIUser(user)), nil
}

func avatarError(err error) (api.UploadCurrentUserAvatarResponseObject, error) {
	var limit *http.MaxBytesError
	switch {
	case errors.As(err, &limit), errors.Is(err, usecase.ErrAvatarTooLarge):
		return api.UploadCurrentUserAvatar413JSONResponse{Code: api.UserProfileErrorCodePayloadTooLarge, Message: "avatar upload exceeds size limit"}, nil
	case errors.Is(err, usecase.ErrUnsupportedMediaType):
		return api.UploadCurrentUserAvatar415JSONResponse{Code: api.UserProfileErrorCodeUnsupportedMediaType, Message: "only matching JPEG and PNG content types are supported"}, nil
	case errors.Is(err, usecase.ErrUserNotFound):
		return api.UploadCurrentUserAvatar401JSONResponse{Code: api.UserProfileErrorCodeUnauthorized, Message: "user no longer exists"}, nil
	case errors.Is(err, usecase.ErrInvalidImage), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), err == nil:
		return api.UploadCurrentUserAvatar400JSONResponse{Code: api.UserProfileErrorCodeValidationError, Message: "invalid avatar upload"}, nil
	default:
		// Multipart parsing errors are handled separately from infrastructure failures.
		return nil, err
	}
}

// Malformed multipart syntax is user input, not an infrastructure failure.
func avatarParseError(err error) (api.UploadCurrentUserAvatarResponseObject, error) {
	var limit *http.MaxBytesError
	if errors.As(err, &limit) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return avatarError(err)
	}
	return avatarError(usecase.ErrInvalidImage)
}
