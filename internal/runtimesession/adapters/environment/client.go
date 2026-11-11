// Package environment адаптирует существующий provisioning без публикации его секретов.
package environment

import (
	"context"
	"errors"

	R "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	U "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
)

// Client адаптирует готовый сервис окружений к портам пользовательских runtime-сессий.
type Client struct{ service U.EnvironmentService }

// New подключает готовый сервис окружений к сценариям runtime-сессий.
func New(s U.EnvironmentService) *Client { return &Client{s} }

// mapError переводит ошибки сервиса окружений в ошибки пользовательских runtime-сессий.
func mapError(err error) error {
	switch {
	case errors.Is(err, U.ErrNotFound):
		return D.ErrNotFound
	case errors.Is(err, U.ErrConflict):
		return D.ErrConflict
	case errors.Is(err, U.ErrCapacity):
		return D.ErrQueueFull
	case errors.Is(err, U.ErrUnknownProfile), errors.Is(err, U.ErrInvalidLanguage), errors.Is(err, U.ErrInvalidSession):
		return D.ErrInvalid
	default:
		return err
	}
}

// safe оставляет только состояние и безопасную причину ошибки окружения без адресов и секретов.
func safe(e R.Environment) D.Environment {
	reason := ""
	if e.LastError != "" {
		reason = "environment_failed"
	}
	return D.Environment{SessionID: e.SessionID, State: string(e.State), ReasonCode: reason}
}

// RequestCreate сохраняет запрос подготовки окружения через готовый сервис #57, не ожидая создания VPS.
func (c *Client) RequestCreate(ctx context.Context, r D.EnvironmentRequest) (D.Environment, error) {
	e, err := c.service.RequestCreate(ctx, U.CreateEnvironment{SessionID: r.SessionID, Language: r.Language, Profile: r.Profile})
	return safe(e), mapError(err)
}

// Get читает состояние окружения по UUID сессии.
func (c *Client) Get(ctx context.Context, id uuid.UUID) (D.Environment, error) {
	e, err := c.service.Get(ctx, id)
	return safe(e), mapError(err)
}

// RequestDelete запрашивает удаление окружения; подтверждение запроса ещё не означает завершение очистки.
func (c *Client) RequestDelete(ctx context.Context, id uuid.UUID, reason string) error {
	return mapError(c.service.RequestDelete(ctx, id, reason))
}
