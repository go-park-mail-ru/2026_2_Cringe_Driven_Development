package usecase

import (
	"context"
	"errors"
	"time"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
)

// Queue читает только незавершённые исполнения ячеек: выполняемое первым, затем ожидающие; инспекции не
// включаются.
func (s *Service) Queue(ctx context.Context, owner int64, id uuid.UUID) (D.Queue, error) {
	return s.store.Queue(ctx, owner, id)
}

// ExecuteBatch проверяет запрос Run all, задаёт таймаут по умолчанию и передаёт атомарное принятие пакета
// хранилищу.
func (s *Service) ExecuteBatch(ctx context.Context, owner int64, id uuid.UUID, r D.BatchRequest) (D.Batch, error) {
	if r.TimeoutSeconds == 0 {
		r.TimeoutSeconds = 300
	}
	if err := r.Validate(); err != nil {
		return D.Batch{}, err
	}
	return s.store.AdmitBatch(ctx, owner, id, r)
}

// Inspect проверяет ID команды и сохраняет инспекцию до её отправки агенту.
func (s *Service) Inspect(ctx context.Context, owner int64, id uuid.UUID, r D.InspectRequest) (D.Inspection, error) {
	if err := r.Validate(); err != nil {
		return D.Inspection{}, err
	}
	x, err := s.store.AdmitInspection(ctx, owner, id, r.ExecutionID)
	if err != nil {
		return D.Inspection{}, err
	}
	return x.Inspection()
}

// GetInspection читает сохранённый результат инспекции; кодовое исполнение с тем же ID инспекцией не
// считается.
func (s *Service) GetInspection(ctx context.Context, owner int64, id uuid.UUID, executionID string) (D.Inspection, error) {
	x, err := s.store.Execution(ctx, owner, id, executionID)
	if err != nil {
		return D.Inspection{}, err
	}
	return x.Inspection()
}

// Kernel выполняет ограниченный по времени interrupt или restart под общей блокировкой приёма команд.
func (s *Service) Kernel(ctx context.Context, owner int64, id uuid.UUID, restart bool) (string, error) {
	a, ok := s.agent.(KernelAgent)
	if !ok {
		return "", D.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	generation, err := s.store.Control(ctx, owner, id, restart, func(ctx context.Context) (string, error) {
		if restart {
			return a.Restart(ctx, id)
		}
		return "", a.Interrupt(ctx, id)
	})
	if err != nil && !errors.Is(err, D.ErrNotFound) && !errors.Is(err, D.ErrConflict) {
		return "", D.ErrUnavailable
	}
	return generation, err
}
