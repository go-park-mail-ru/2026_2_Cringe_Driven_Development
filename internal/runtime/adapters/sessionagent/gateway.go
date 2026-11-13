// Package sessionagent предоставляет сессиям доступ к уже подготовленному агенту.
package sessionagent

import (
	"context"
	"errors"
	"net/http"

	U "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
)

// Client определяет доступные операции внутреннего клиента агента.
type Client interface {
	// Health читает готовность агента, поколение kernel и состояние его очереди.
	Health(context.Context, U.AgentEndpoint) (U.AgentHealth, int, error)
	// ExecuteCommand передаёт агенту исполнение кода или инспекцию с неизменным execution_id и проверяет
	// подтверждение.
	ExecuteCommand(context.Context, U.AgentEndpoint, D.Command) (D.AgentExecution, error)
	// ReadExecution запрашивает состояние и результат конкретной команды агента по её ID.
	ReadExecution(context.Context, U.AgentEndpoint, string) (D.AgentExecution, error)
	// ReadEvents получает следующую страницу событий агента, сохраняя порядок и целостность большого первого
	// события.
	ReadEvents(context.Context, U.AgentEndpoint, int64, D.EventLimits) ([]D.AgentEvent, error)
	// Activity передаёт агенту реальное действие пользователя, продлевающее время активности.
	Activity(context.Context, U.AgentEndpoint) error
	// Drain запрещает новые команды у агента и запрашивает остановку выполняющейся команды перед завершением.
	Drain(context.Context, U.AgentEndpoint) error
	// Stop сохраняет намерение завершить сессию; фактическую остановку и очистку выполняет фоновый обработчик.
	Stop(context.Context, U.AgentEndpoint) error
}

// Gateway получает подготовленный адрес и расшифрованный токен агента из модуля окружений, не раскрывая их
// клиенту.
type Gateway struct {
	// repo Хранилище окружений и их внутренних учётных данных.
	repo U.EnvironmentRepository
	// secrets Средство расшифровки внутреннего токена агента.
	secrets U.Secrets
	// client Клиент внутреннего API агента.
	client Client
}

// New связывает хранилище окружений, расшифровку токенов и клиент агента.
func New(r U.EnvironmentRepository, s U.Secrets, c Client) *Gateway { return &Gateway{r, s, c} }

// endpoint получает адрес подготовленного агента и расшифровывает его токен только для внутреннего вызова.
func (g *Gateway) endpoint(ctx context.Context, id uuid.UUID) (U.AgentEndpoint, error) {
	if g.client == nil || g.repo == nil || g.secrets == nil {
		return U.AgentEndpoint{}, D.ErrUnavailable
	}
	env, err := g.repo.Get(ctx, id)
	if errors.Is(err, U.ErrNotFound) {
		return U.AgentEndpoint{}, D.ErrNotFound
	}
	if err != nil {
		return U.AgentEndpoint{}, err
	}
	if env.Endpoint == "" {
		return U.AgentEndpoint{}, D.ErrUnavailable
	}
	sealed, err := g.repo.LoadSecrets(ctx, id)
	if err != nil {
		return U.AgentEndpoint{}, D.ErrUnavailable
	}
	token, err := g.secrets.Open(sealed.Token)
	if err != nil {
		return U.AgentEndpoint{}, D.ErrUnavailable
	}
	return U.AgentEndpoint{BaseURL: env.Endpoint, Token: string(token)}, nil
}

// Health читает готовность агента, поколение kernel и состояние его очереди.
func (g *Gateway) Health(ctx context.Context, id uuid.UUID) (D.Health, error) {
	ep, err := g.endpoint(ctx, id)
	if err != nil {
		return D.Health{}, err
	}
	h, status, err := g.client.Health(ctx, ep)
	if err != nil {
		return D.Health{}, err
	}
	if status != http.StatusOK && status != http.StatusServiceUnavailable {
		return D.Health{}, D.ErrUnavailable
	}
	sid, err := uuid.Parse(h.SessionID)
	if err != nil {
		return D.Health{}, D.ErrProtocol
	}
	return D.Health{SessionID: sid, Language: h.Language, State: h.State, Ready: h.Ready, Reason: h.Reason, Generation: h.Generation, Queued: h.Queued, Draining: h.Draining, ActiveExecutionID: h.ActiveExecutionID}, nil
}

// Execute передаёт сохранённую команду агенту; неизменный ID позволяет безопасно повторить сетевой запрос.
func (g *Gateway) Execute(ctx context.Context, id uuid.UUID, c D.Command) (D.AgentExecution, error) {
	ep, err := g.endpoint(ctx, id)
	if err != nil {
		return D.AgentExecution{}, err
	}
	return g.client.ExecuteCommand(ctx, ep, c)
}

// Execution читает состояние и результат команды из внутреннего API агента.
func (g *Gateway) Execution(ctx context.Context, id uuid.UUID, x string) (D.AgentExecution, error) {
	ep, err := g.endpoint(ctx, id)
	if err != nil {
		return D.AgentExecution{}, err
	}
	return g.client.ReadExecution(ctx, ep, x)
}

// Events читает очередную страницу внутреннего журнала агента после указанного seq.
func (g *Gateway) Events(ctx context.Context, id uuid.UUID, after int64, limits D.EventLimits) ([]D.AgentEvent, error) {
	ep, err := g.endpoint(ctx, id)
	if err != nil {
		return nil, err
	}
	return g.client.ReadEvents(ctx, ep, after, limits)
}

// Activity передаёт агенту реальное действие пользователя, продлевающее время активности.
func (g *Gateway) Activity(ctx context.Context, id uuid.UUID) error {
	ep, err := g.endpoint(ctx, id)
	if err != nil {
		return err
	}
	return g.client.Activity(ctx, ep)
}

// Drain запрещает новые команды у агента и запрашивает остановку выполняющейся команды перед завершением.
func (g *Gateway) Drain(ctx context.Context, id uuid.UUID) error {
	ep, err := g.endpoint(ctx, id)
	if err != nil {
		return err
	}
	return g.client.Drain(ctx, ep)
}

// Stop просит агента остановить kernel, сохранив доступ к его журналу.
func (g *Gateway) Stop(ctx context.Context, id uuid.UUID) error {
	ep, err := g.endpoint(ctx, id)
	if err != nil {
		return err
	}
	return g.client.Stop(ctx, ep)
}

// Interrupt запрашивает прерывание текущей команды без автоматического повтора при потере ответа.
func (g *Gateway) Interrupt(ctx context.Context, id uuid.UUID) error {
	ep, err := g.endpoint(ctx, id)
	if err != nil {
		return err
	}
	c, ok := g.client.(interface {
		Interrupt(context.Context, U.AgentEndpoint) error
	})
	if !ok {
		return D.ErrUnavailable
	}
	return c.Interrupt(ctx, ep)
}

// Restart перезапускает свободный kernel и возвращает новое поколение; потерянный ответ автоматически не
// повторяется.
func (g *Gateway) Restart(ctx context.Context, id uuid.UUID) (string, error) {
	ep, err := g.endpoint(ctx, id)
	if err != nil {
		return "", err
	}
	c, ok := g.client.(interface {
		Restart(context.Context, U.AgentEndpoint) (string, error)
	})
	if !ok {
		return "", D.ErrUnavailable
	}
	return c.Restart(ctx, ep)
}

// StopExecutions останавливает команды без завершения окружения.
func (g *Gateway) StopExecutions(ctx context.Context, id uuid.UUID, operation string, executionIDs []string) error {
	ep, err := g.endpoint(ctx, id)
	if err != nil {
		return err
	}
	c, ok := g.client.(interface {
		StopExecutions(context.Context, U.AgentEndpoint, string, []string) error
	})
	if !ok {
		return D.ErrUnavailable
	}
	return c.StopExecutions(ctx, ep, operation, executionIDs)
}
