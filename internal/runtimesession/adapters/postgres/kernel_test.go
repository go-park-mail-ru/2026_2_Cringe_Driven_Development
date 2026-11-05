package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
)

// TestRestartSerializesWithConcurrentAdmission проверяет, что новая команда ждёт завершения restart под
// общей блокировкой.
func TestRestartSerializesWithConcurrentAdmission(t *testing.T) {
	s, _, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, err := s.Start(ctx, newSession(owner, nb))
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	controlDone := make(chan error, 1)
	generation := uuid.NewString()
	go func() {
		_, controlErr := s.Control(ctx, owner, v.ID, true, func(ctx context.Context) (string, error) {
			close(entered)
			select {
			case <-release:
				return generation, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		})
		controlDone <- controlErr
	}()
	<-entered
	hash := sha256.Sum256([]byte("print(42)"))
	admitDone := make(chan error, 1)
	go func() {
		_, admitErr := s.Admit(ctx, owner, v.ID, D.ExecuteRequest{ExecutionID: "after-restart", CellID: "a", SourceHash: hex.EncodeToString(hash[:]), TimeoutSeconds: 300})
		admitDone <- admitErr
	}()
	select {
	case admitErr := <-admitDone:
		close(release)
		t.Fatal("admission crossed restart", admitErr)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err = <-controlDone; err != nil {
		t.Fatal(err)
	}
	if err = <-admitDone; err != nil {
		t.Fatal(err)
	}
	got, err := s.Owned(ctx, owner, v.ID)
	if err != nil || got.Generation != generation {
		t.Fatal(got, err)
	}
}
