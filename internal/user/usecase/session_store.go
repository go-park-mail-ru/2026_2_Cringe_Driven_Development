package usecase

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidSession = errors.New("session is invalid or expired")
	ErrVersionChanged = errors.New("session version has changed")
)

// SessionVersion хранит версию отзыва пользовательских сессий.
type SessionVersion uint64

// SessionClaims связывает владельца, идентификатор сессии и действующую версию.
type SessionClaims struct {
	UserID    int64
	SessionID string
	Version   SessionVersion
}

// RefreshSession содержит данные сессии, найденной по хешу refresh-токена.
type RefreshSession struct {
	SessionClaims
	TokenHash string
	ExpiresAt time.Time
}

// NewSession передаёт данные и срок жизни новой записи сессии.
type NewSession struct {
	RefreshSession
	AccessExpiresAt time.Time
}

// SessionStore хранит основное состояние access- и refresh-авторизации.
// Чтения никогда не инициализируют версии и не продлевают TTL. Мутации должны быть атомарны.
// Транспортная ошибка при мутации может означать неизвестный исход, а не откат.
type SessionStore interface {
	CurrentVersion(context.Context, int64) (SessionVersion, error)
	Create(context.Context, NewSession) error
	Find(context.Context, string) (RefreshSession, error)
	ValidateAccess(context.Context, SessionClaims) error
	Rotate(context.Context, string, NewSession) error
	Revoke(context.Context, string, SessionClaims) error
	RevokeAll(context.Context, int64) error
}
