package usecase

import (
	"context"
	"errors"
	"testing"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
	"go.uber.org/mock/gomock"
)

// fileAgentStub выявляет обращения к агенту до проверки владельца и готовности.
type fileAgentStub struct {
	SessionAgent
	calls int
}

// Files фиксирует только допущенные обращения.
func (a *fileAgentStub) Files(context.Context, uuid.UUID, FileRequest) (FileResponse, error) {
	a.calls++
	return FileResponse{Status: 200}, nil
}

// TestFilesRequireOwnedReadySession проверяет запрет файлового доступа по одному UUID.
func TestFilesRequireOwnedReadySession(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state D.State
		err   error
		calls int
	}{
		{"foreign", D.Ready, D.ErrNotFound, 0}, {"stopped", D.Stopped, nil, 0}, {"starting", D.Starting, nil, 0}, {"ready", D.Ready, nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewMockStore(gomock.NewController(t))
			id := uuid.New()
			agent := &fileAgentStub{}
			store.EXPECT().Owned(gomock.Any(), int64(7), id).Return(D.Session{ID: id, State: tc.state}, tc.err)
			_, err := NewService(store, nil, agent).Files(context.Background(), 7, id, FileRequest{})
			if agent.calls != tc.calls {
				t.Fatalf("calls=%d", agent.calls)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatal(err)
			}
			if tc.state != D.Ready && err == nil {
				t.Fatal("unready session admitted")
			}
		})
	}
}

// TestFilesUnavailableDependencies проверяет отсутствие обхода владельца и недоступный файловый порт.
func TestFilesUnavailableDependencies(t *testing.T) {
	store := NewMockStore(gomock.NewController(t))
	id := uuid.New()
	service := NewService(store, nil)
	if _, err := service.Files(context.Background(), 0, id, FileRequest{}); !errors.Is(err, D.ErrNotFound) {
		t.Fatal(err)
	}
	store.EXPECT().Owned(gomock.Any(), int64(7), id).Return(D.Session{State: D.Ready}, nil)
	if _, err := service.Files(context.Background(), 7, id, FileRequest{}); !errors.Is(err, D.ErrUnavailable) {
		t.Fatal(err)
	}
}
