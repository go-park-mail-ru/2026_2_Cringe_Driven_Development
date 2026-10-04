// Package usecase содержит бизнес-логику пользователя: регистрацию, вход, сессии.
// Не знает ни про HTTP, ни про то, какая база под repository.Repository.
package usecase

import (
	"context"
	"errors"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/models"
)

var (
	ErrLoginTaken         = errors.New("login is taken")
	ErrInvalidCredentials = errors.New("invalid login or password")
	// ErrPasswordTooLong: bcrypt считает байты, а контракт символы.
	// 72 символа кириллицей занимают 144 байта.
	ErrPasswordTooLong = errors.New("password is longer than 72 bytes")
	ErrInvalidRefresh  = errors.New("refresh token is invalid or expired")
	ErrUserNotFound    = errors.New("user not found")
)

type Usecase interface {
	Register(ctx context.Context, login, password string) (models.User, models.Session, error)
	Login(ctx context.Context, login, password string) (models.User, models.Session, error)
	// Refresh делает ротацию: старая сессия удаляется, вместо неё создаётся новая.
	Refresh(ctx context.Context, refreshToken string) (models.Session, error)
	Logout(ctx context.Context, refreshToken string) error
	CurrentUser(ctx context.Context, id int64) (models.User, error)
}

type TokenIssuer interface {
	Issue(userID int64) (string, error)
}
