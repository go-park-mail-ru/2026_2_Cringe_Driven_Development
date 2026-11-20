package usecase

import (
	"context"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
)

// FileAgent передаёт файлы подготовленному агенту с ограниченной буферизацией.
type FileAgent interface {
	Files(context.Context, uuid.UUID, FileRequest) (FileResponse, error)
}

// Files проверяет владельца и готовность runtime перед каждой файловой командой или частью.
func (s *Service) Files(ctx context.Context, owner int64, id uuid.UUID, req FileRequest) (FileResponse, error) {
	if owner <= 0 {
		return FileResponse{}, D.ErrNotFound
	}
	session, err := s.store.Owned(ctx, owner, id)
	if err != nil {
		return FileResponse{}, err
	}
	if session.State != D.Ready {
		if session.State == D.Stopping || session.State == D.Stopped || session.State == D.Failed {
			return FileResponse{}, D.ErrWorkspaceClosed
		}
		return FileResponse{}, D.ErrUnavailable
	}
	agent, ok := s.agent.(FileAgent)
	if !ok {
		return FileResponse{}, D.ErrUnavailable
	}
	return agent.Files(ctx, id, req)
}
