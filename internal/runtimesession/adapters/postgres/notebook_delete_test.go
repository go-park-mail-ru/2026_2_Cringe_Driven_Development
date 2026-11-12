package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"

	notebookusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
)

func TestNotebookDeletionAndSessionStartSerialize(t *testing.T) {
	for range 6 {
		s, pool, owner, id := integrationStore(t)
		ctx := context.Background()
		start := make(chan struct{})
		var wg sync.WaitGroup
		var deleteErr, startErr error
		wg.Go(func() { <-start; deleteErr = s.notebooks.DeleteNotebook(ctx, id, owner) })
		wg.Go(func() { <-start; _, startErr = s.Start(ctx, newSession(owner, id)) })
		close(start)
		wg.Wait()
		switch {
		case deleteErr == nil:
			if !errors.Is(startErr, domain.ErrNotFound) {
				t.Fatalf("start after delete: %v", startErr)
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime_sessions WHERE notebook_id=$1`, id).Scan(&count); err != nil || count != 0 {
				t.Fatalf("orphan session: %d %v", count, err)
			}
		case errors.Is(deleteErr, notebookusecase.ErrNotebookInUse):
			if startErr != nil {
				t.Fatal(startErr)
			}
			if _, err := s.notebooks.Get(ctx, id, owner); err != nil {
				t.Fatal("active notebook deleted", err)
			}
		default:
			t.Fatalf("delete: %v start: %v", deleteErr, startErr)
		}
	}
}

// Отклонённая команда никогда не попадает в очередь проекции, даже если её флаг отображения ожидает.
func TestNotebookDeletionAfterRejectedExecution(t *testing.T) {
	s, pool, owner, id := integrationStore(t)
	ctx := context.Background()
	session, err := s.Start(ctx, newSession(owner, id))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("print(42)"))
	execution, err := s.Admit(ctx, owner, session.ID, domain.ExecuteRequest{ExecutionID: "rejected", CellID: "cell", SourceHash: hex.EncodeToString(hash[:]), TimeoutSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.Claim(ctx, "reject-worker")
	if err != nil {
		t.Fatal(err)
	}
	execution.State = "REJECTED"
	if err = s.SaveExecution(ctx, lease, execution); err != nil {
		t.Fatal(err)
	}
	if err = s.Mutate(ctx, owner, session.ID, func(v *domain.Session) error { v.State = domain.Stopped; v.CleanupError = false; return nil }); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM runtime_projections WHERE session_id=$1`, session.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unexpected projection: %d %v", count, err)
	}
	if err = s.notebooks.DeleteNotebook(ctx, id, owner); err != nil {
		t.Fatalf("rejected execution blocked deletion: %v", err)
	}
}

func TestNotebookDeletionWaitsForActualProjection(t *testing.T) {
	s, _, owner, id := integrationStore(t)
	ctx := context.Background()
	session, err := s.Start(ctx, newSession(owner, id))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("print(42)"))
	execution, err := s.Admit(ctx, owner, session.ID, domain.ExecuteRequest{ExecutionID: "completed", CellID: "cell", SourceHash: hex.EncodeToString(hash[:]), TimeoutSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.Claim(ctx, "result-worker")
	if err != nil {
		t.Fatal(err)
	}
	execution.State = "SUCCEEDED"
	if err = s.SaveExecution(ctx, lease, execution); err != nil {
		t.Fatal(err)
	}
	if err = s.Mutate(ctx, owner, session.ID, func(v *domain.Session) error { v.State = domain.Failed; v.CleanupError = false; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = s.notebooks.DeleteNotebook(ctx, id, owner); !errors.Is(err, notebookusecase.ErrNotebookInUse) {
		t.Fatalf("pending projection: %v", err)
	}
	if err = s.Project(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.notebooks.DeleteNotebook(ctx, id, owner); err != nil {
		t.Fatalf("completed projection: %v", err)
	}
	if err = s.Project(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("late projection: %v", err)
	}
}
