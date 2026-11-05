package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
)

// TestConcurrentStartsDifferentNotebooksOneOwner проверяет единственный слот пользователя при одновременном
// запуске разных блокнотов.
func TestConcurrentStartsDifferentNotebooksOneOwner(t *testing.T) {
	s, pool, owner, nb := integrationStore(t)
	ctx := context.Background()
	var nb2 int64
	if err := pool.QueryRow(ctx, "INSERT INTO notebooks(owner_id,name,revision) VALUES($1,'second','revision2') RETURNING id", owner).Scan(&nb2); err != nil {
		t.Fatal(err)
	}
	if v, activeErr := s.Active(ctx, owner); activeErr != nil || v != nil {
		t.Fatal(v, activeErr)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []int64{nb, nb2} {
		wg.Go(func() { _, err := s.Start(ctx, newSession(owner, id)); results <- err })
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
			continue
		}
		var conflict *D.ActiveSessionError
		if !errors.As(err, &conflict) || conflict.Session.OwnerID != owner {
			t.Fatal(err)
		}
		conflicts++
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal(wins, conflicts)
	}
	active, err := s.Active(ctx, owner)
	if err != nil || active == nil {
		t.Fatal(active, err)
	}
	repeated, err := s.Start(ctx, *active)
	if err != nil || repeated.ID != active.ID {
		t.Fatal("idempotent retry", repeated, err)
	}
	if err = s.Mutate(ctx, owner, active.ID, func(v *D.Session) error { v.State = D.Stopping; return nil }); err != nil {
		t.Fatal(err)
	}
	if v, activeErr := s.Active(ctx, owner); activeErr != nil || v == nil || v.State != D.Stopping {
		t.Fatal(v, activeErr)
	}
	if err = s.Mutate(ctx, owner, active.ID, func(v *D.Session) error { v.State = D.Stopped; return nil }); err != nil {
		t.Fatal(err)
	}
	if v, activeErr := s.Active(ctx, owner); activeErr != nil || v != nil {
		t.Fatal(v, activeErr)
	}
}

// TestDifferentOwnersCanStartIndependently проверяет независимые слоты разных пользователей.
func TestDifferentOwnersCanStartIndependently(t *testing.T) {
	s, p, owner, nb := integrationStore(t)
	ctx := context.Background()
	var owner2, nb2 int64
	if err := p.QueryRow(ctx, "INSERT INTO users(login,password_hash) VALUES('other','hash') RETURNING id").Scan(&owner2); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO notebooks(owner_id,name,revision) VALUES($1,'other','other') RETURNING id", owner2).Scan(&nb2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(ctx, newSession(owner, nb)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(ctx, newSession(owner2, nb2)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(ctx, newSession(owner, nb2)); !errors.Is(err, D.ErrNotFound) {
		t.Fatal("foreign notebook accepted", err)
	}
}

// TestPublicCursorPaginationKeepsLargeFirstEvent проверяет целостность первого большого события и корректное
// продолжение публичного курсора.
func TestPublicCursorPaginationKeepsLargeFirstEvent(t *testing.T) {
	s, p, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, err := s.Start(ctx, newSession(owner, nb))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if _, err = p.Exec(ctx, "INSERT INTO runtime_events(session_id,public_seq,kind,body) VALUES($1,$2,'test',$3)", v.ID, i, `{"text":"abcdefghijklmnop"}`); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.Events(ctx, owner, v.ID, 0, D.EventLimits{Count: 2, Bytes: 10})
	if err != nil || len(page.Events) != 1 || page.NextCursor != 1 || !page.HasMore {
		t.Fatal(page, err)
	}
	page, err = s.Events(ctx, owner, v.ID, page.NextCursor, D.EventLimits{Count: 2, Bytes: 1000})
	if err != nil || len(page.Events) != 2 || page.NextCursor != 3 || page.HasMore {
		t.Fatal(page, err)
	}
}

// TestPendingDispatchInAdmissionOrderAcrossCells проверяет порядок отправки по факту принятия, а не по
// ячейке или её номеру попытки.
func TestPendingDispatchInAdmissionOrderAcrossCells(t *testing.T) {
	s, p, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, err := s.Start(ctx, newSession(owner, nb))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("print(42)"))
	for _, item := range []struct{ id, cell string }{{"z", "first"}, {"a", "second"}, {"repeat", "first"}, {"b", "third"}} {
		_, err = s.Admit(ctx, owner, v.ID, D.ExecuteRequest{ExecutionID: item.id, CellID: item.cell, SourceHash: hex.EncodeToString(sum[:]), TimeoutSeconds: 300})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range []string{"z", "a", "repeat", "b"} {
		x, err := s.NextPending(ctx, v.ID)
		if err != nil || x.ID != want {
			t.Fatalf("dispatch[%d]=%s, want %s: %v", i, x.ID, want, err)
		}
		if _, err = p.Exec(ctx, `UPDATE runtime_executions SET data=jsonb_set(data,'{Dispatch}','"ACCEPTED"') WHERE session_id=$1 AND id=$2`, v.ID, x.ID); err != nil {
			t.Fatal(err)
		}
	}
}
