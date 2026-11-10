package usecase

import (
	"context"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/google/uuid"
)

const defaultProfile = "default"

// Settings — настройки сервиса.
type Settings struct {
	MaxActive    int
	AgentVersion string
	Profiles     map[string]bool
}

// Service реализует EnvironmentService.
type Service struct {
	repo EnvironmentRepository
	s    Settings
}

// NewService создаёт сервис.
func NewService(repo EnvironmentRepository, s Settings) *Service {
	return &Service{repo: repo, s: s}
}

var _ EnvironmentService = (*Service)(nil)

func normProfile(p string) string {
	if p == "" {
		return defaultProfile
	}
	return p
}

// RequestCreate сохраняет окружение и задание, не ожидая облака.
func (s *Service) RequestCreate(ctx context.Context, req CreateEnvironment) (domain.Environment, error) {
	if req.SessionID == uuid.Nil {
		return domain.Environment{}, ErrInvalidSession
	}
	if !domain.ValidLanguage(req.Language) {
		return domain.Environment{}, ErrInvalidLanguage
	}
	profile := normProfile(req.Profile)
	if !s.s.Profiles[profile] {
		return domain.Environment{}, ErrUnknownProfile
	}
	env := domain.Environment{
		SessionID:    req.SessionID,
		OperationID:  uuid.New(),
		Language:     req.Language,
		Profile:      profile,
		State:        domain.StateQueued,
		DesiredState: domain.StateReady,
		AgentVersion: s.s.AgentVersion,
	}
	got, created, err := s.repo.CreateWithJob(ctx, env, s.s.MaxActive)
	if err != nil {
		return domain.Environment{}, err
	}
	if !created && (got.Language != req.Language || normProfile(got.Profile) != profile) {
		return domain.Environment{}, ErrConflict
	}
	return got, nil
}

// Get возвращает окружение сессии.
func (s *Service) Get(ctx context.Context, sessionID uuid.UUID) (domain.Environment, error) {
	return s.repo.Get(ctx, sessionID)
}

// RequestDelete идемпотентно запрашивает удаление.
func (s *Service) RequestDelete(ctx context.Context, sessionID uuid.UUID, reason string) error {
	return s.repo.MarkForDelete(ctx, sessionID, reason)
}
