// Package repository описывает хранилище пользователей и сессий.
// Реализации лежат во вложенных пакетах: postgres, позже redis.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/models"
)

var (
	ErrLoginTaken      = errors.New("login is taken")
	ErrUserNotFound    = errors.New("user not found")
	ErrSessionNotFound = errors.New("refresh session not found")
)

type Repository interface {
	CreateUser(ctx context.Context, login, passwordHash string) (models.User, error)
	// UserByLogin ищет без учёта регистра, как и уникальный индекс.
	UserByLogin(ctx context.Context, login string) (u models.User, passwordHash string, err error)
	UserByID(ctx context.Context, id int64) (models.User, error)
	CreateSession(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error
	SessionByTokenHash(ctx context.Context, tokenHash string) (userID int64, expiresAt time.Time, err error)
	DeleteSession(ctx context.Context, tokenHash string) (userID int64, expiresAt time.Time, err error)
}
