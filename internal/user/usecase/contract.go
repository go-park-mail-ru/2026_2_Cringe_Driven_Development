// Package usecase содержит бизнес-логику пользователя: регистрацию, вход, сессии.
// Не знает ни про HTTP, ни про то, какие хранилища реализуют прикладные порты.
package usecase

import (
	"context"
	"errors"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
)

var (
	ErrValidation             = errors.New("invalid profile input")
	ErrInvalidCurrentPassword = errors.New("invalid current password")
	ErrLoginTaken             = errors.New("login is taken")
	ErrInvalidCredentials     = errors.New("invalid login or password")
	ErrInvalidRefresh         = errors.New("refresh token is invalid or expired")
	ErrUserNotFound           = errors.New("user not found")
)

type Usecase interface {
	EnableTwoFactor(ctx context.Context, id int64) (TwoFactorSetup, error)
	ConfirmTwoFactor(ctx context.Context, id int64, code string) error
	DisableTwoFactor(ctx context.Context, id int64, password string) error
	UpdateCurrentUser(ctx context.Context, id int64, login string) (domain.User, error)
	ChangeCurrentUserPassword(ctx context.Context, id int64, currentPassword, newPassword string) error
	Register(ctx context.Context, login, password string) (domain.User, Session, error)
	Login(ctx context.Context, login, password string, userCode ...*string) (domain.User, Session, error)
	// Refresh атомарно меняет refresh-токен, сохраняя sid и версию.
	Refresh(ctx context.Context, refreshToken string) (Session, error)
	Logout(ctx context.Context, refreshToken string, claims SessionClaims) error
	RevokeAll(ctx context.Context, userID int64) error
	CurrentUser(ctx context.Context, id int64) (domain.User, error)
}

type TokenIssuer interface {
	IssueAccess(claims SessionClaims) (token string, expiresAt time.Time, err error)
}
