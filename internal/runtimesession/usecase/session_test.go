package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
	"go.uber.org/mock/gomock"
)

// TestStartStoresIntentBeforeProvisioning проверяет запись намерения запуска с UUID и начальным состоянием
// до обращения к облаку.
func TestStartStoresIntentBeforeProvisioning(t *testing.T) {
	s := NewMockStore(gomock.NewController(t))
	now := time.Unix(100, 0)
	s.EXPECT().Start(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, v D.Session) (D.Session, error) {
		if v.ID == uuid.Nil || v.State != D.Provisioning || v.Stage != "queued" || v.OwnerID != 7 || !v.CreatedAt.Equal(now) {
			t.Fatal(v)
		}
		return v, nil
	})
	_, err := NewService(s, func() time.Time { return now }).Start(context.Background(), 7, 9, D.StartRequest{RequestID: "request", Language: "python"})
	if err != nil {
		t.Fatal(err)
	}
}

// TestActivityDoesNotReviveStoppedRuntime проверяет, что уведомление об активности не возобновляет
// завершённую сессию.
func TestActivityDoesNotReviveStoppedRuntime(t *testing.T) {
	s := NewMockStore(gomock.NewController(t))
	id := uuid.New()
	s.EXPECT().Mutate(gomock.Any(), int64(7), id, gomock.Any()).DoAndReturn(func(_ context.Context, _ int64, _ uuid.UUID, fn func(*D.Session) error) error {
		return fn(&D.Session{State: D.Stopped})
	})
	if err := NewService(s, nil).Activity(context.Background(), 7, id); !errors.Is(err, D.ErrConflict) {
		t.Fatal(err)
	}
}

// TestGetActiveReturnsStoreError проверяет, что ошибка чтения активной сессии не подменяется пустым успешным
// ответом.
func TestGetActiveReturnsStoreError(t *testing.T) {
	s := NewMockStore(gomock.NewController(t))
	fail := errors.New("db unavailable")
	s.EXPECT().Active(gomock.Any(), int64(7)).Return(nil, fail)
	_, err := NewService(s, nil).GetActive(context.Background(), 7)
	if !errors.Is(err, fail) {
		t.Fatal(err)
	}
}
