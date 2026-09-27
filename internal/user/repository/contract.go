// Package repository описывает хранилище пользователей и сессий.
// Реализации лежат во вложенных пакетах: postgres, позже redis.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/models"
	"github.com/google/uuid"
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
	UserByID(ctx context.Context, id uuid.UUID) (models.User, error)
	CreateSession(ctx context.Context, userID uuid.UUID, tokenHash string, expiresAt time.Time) error
	DeleteSession(ctx context.Context, tokenHash string) (userID uuid.UUID, expiresAt time.Time, err error)
}
