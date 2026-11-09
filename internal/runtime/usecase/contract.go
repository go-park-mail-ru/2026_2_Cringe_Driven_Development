// Package usecase содержит сценарии создания, настройки и удаления окружений (VPS) пользовательских сессий.
package usecase

import (
	"context"
	"errors"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/google/uuid"
)

var (
	ErrNotFound        = errors.New("environment not found")
	ErrConflict        = errors.New("environment already exists with other parameters")
	ErrInvalidLanguage = errors.New("invalid kernel language")
	ErrUnknownProfile  = errors.New("unknown environment profile")
	ErrCapacity        = errors.New("active environments limit reached")
	ErrDisabled        = errors.New("runtime provisioning is disabled")
	ErrInvalidSession  = errors.New("invalid session id")
)

// CreateEnvironment — запрос на создание окружения.
type CreateEnvironment struct {
	SessionID uuid.UUID
	Language  string // только python
	Profile   string // серверный allowlist, для MVP: default
}

// EnvironmentService — контракт для потребителей (#13).
type EnvironmentService interface {
	// RequestCreate сохраняет окружение и задание атомарно, не ждёт OpenStack/Ansible.
	RequestCreate(ctx context.Context, req CreateEnvironment) (domain.Environment, error)
	Get(ctx context.Context, sessionID uuid.UUID) (domain.Environment, error)
	// RequestDelete идемпотентно переводит окружение к удалению и создаёт задание.
	RequestDelete(ctx context.Context, sessionID uuid.UUID, reason string) error
}
