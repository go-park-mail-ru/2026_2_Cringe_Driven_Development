package usecase

import (
	"context"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
)

// Settings содержит серверные ограничения доступности пользовательских runtime-сессий.
type Settings struct{ Enabled bool }

// Service реализует пользовательские сценарии через порты, не обращаясь напрямую к HTTP, S3 или OpenStack.
type Service struct {
	// store Хранилище пользовательских сессий, команд и событий.
	store Store
	// settings Серверные настройки runtime-сессий.
	settings Settings
	// now Подменяемые часы для воспроизводимого поведения.
	now Clock
	// agent Внутренний клиент агента.
	agent SessionAgent
}

// NewService собирает сценарии runtime-сессий из портов хранилища, агента и управляемых часов.
func NewService(s Store, c Settings, now Clock, agents ...SessionAgent) *Service {
	if now == nil {
		now = time.Now
	}
	var agent SessionAgent
	if len(agents) > 0 {
		agent = agents[0]
	}
	return &Service{s, c, now, agent}
}

// Start проверяет параметры и сохраняет намерение запуска пользовательской сессии до подготовки VPS.
func (s *Service) Start(ctx context.Context, owner, notebook int64, r domain.StartRequest) (domain.Session, error) {
	if e := r.Validate(); e != nil || owner <= 0 || notebook <= 0 {
		return domain.Session{}, domain.ErrInvalid
	}
	if !s.settings.Enabled {
		return domain.Session{}, domain.ErrUnavailable
	}
	id := uuid.New()
	now := s.now()
	return s.store.Start(ctx, domain.Session{ID: id, OwnerID: owner, NotebookID: notebook, RequestID: r.RequestID, Language: r.Language, State: domain.Provisioning, Stage: "queued", CreatedAt: now, LastActivityAt: now})
}

// GetSession возвращает сессию только после проверки её принадлежности пользователю.
func (s *Service) GetSession(ctx context.Context, owner int64, id uuid.UUID) (domain.Session, error) {
	return s.store.Owned(ctx, owner, id)
}

// GetExecution возвращает исполнение ячейки владельцу, исключая служебные инспекции из этого API.
func (s *Service) GetExecution(ctx context.Context, owner int64, sid uuid.UUID, id string) (domain.Execution, error) {
	x, err := s.store.Execution(ctx, owner, sid, id)
	if err == nil && x.Kind == "inspect" {
		return domain.Execution{}, domain.ErrNotFound
	}
	return x, err
}

// Execute проверяет параметры и сохраняет исполнение ячейки до его отправки агенту фоновым обработчиком.
func (s *Service) Execute(ctx context.Context, owner int64, id uuid.UUID, r domain.ExecuteRequest) (domain.Execution, error) {
	if r.TimeoutSeconds == 0 {
		r.TimeoutSeconds = 300
	}
	if e := r.Validate(); e != nil {
		return domain.Execution{}, e
	}
	return s.store.Admit(ctx, owner, id, r)
}

// Events возвращает события после указанного курсора; чтение не продлевает активность пользователя.
func (s *Service) Events(ctx context.Context, owner int64, id uuid.UUID, after int64) (domain.EventPage, error) {
	if after < 0 {
		return domain.EventPage{}, domain.ErrInvalid
	}
	return s.store.Events(ctx, owner, id, after, domain.EventLimits{Count: 100, Bytes: 16 << 20})
}

// GetActive читает активную runtime-сессию пользователя; ошибка хранилища не подменяется её отсутствием.
func (s *Service) GetActive(ctx context.Context, owner int64) (*domain.Session, error) {
	return s.store.Active(ctx, owner)
}

// Activity сохраняет пользовательскую активность для последующей передачи агенту.
func (s *Service) Activity(ctx context.Context, owner int64, id uuid.UUID) error {
	return s.store.Mutate(ctx, owner, id, func(v *domain.Session) error {
		if v.State != domain.Ready && v.State != domain.Reconnecting {
			return domain.ErrConflict
		}
		v.LastActivityAt = s.now()
		v.ActivityPending = true
		return nil
	})
}

// Stop сохраняет намерение завершить сессию; фактическую остановку и очистку выполняет фоновый обработчик.
func (s *Service) Stop(ctx context.Context, owner int64, id uuid.UUID) error {
	return s.store.Mutate(ctx, owner, id, func(v *domain.Session) error {
		if !v.State.Active() || v.State == domain.Stopping {
			return nil
		}
		v.State = domain.Stopping
		v.Stage = "stopping"
		v.Reason = "requested"
		return nil
	})
}
