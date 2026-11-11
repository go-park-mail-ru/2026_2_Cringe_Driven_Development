package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	nbpg "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/postgres"
	nbD "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// integrationStore создаёт отдельную схему PostgreSQL, выполняет миграции и готовит владельца с блокнотом
// для теста.
func integrationStore(t *testing.T) (*Store, *pgxpool.Pool, int64, int64) {
	t.Helper()
	dsn := os.Getenv("RUNTIME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("RUNTIME_TEST_DATABASE_URL is unset")
	}
	ctx := context.Background()
	root, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	name := "runtime_" + uuid.NewString()[:8]
	schema := pgx.Identifier{name}.Sanitize()
	if _, e = root.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = name
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { pool.Close(); _, _ = root.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); root.Close() })
	if e = migrations.Up(ctx, pool); e != nil {
		t.Fatal(e)
	}
	var owner, nb int64
	if e = pool.QueryRow(ctx, "INSERT INTO users(login,password_hash) VALUES('runtime-test','hash') RETURNING id").Scan(&owner); e != nil {
		t.Fatal(e)
	}

	cells := make([]nbD.Cell, 0, 10)
	for _, id := range []string{"cell", "a", "b", "c", "z", "history", "current", "first", "second", "third"} {
		cells = append(cells, nbD.Cell{ID: id, Kind: nbD.CellKindCode, Source: "print(42)", Outputs: json.RawMessage(`[]`), HasExecutionCount: true})
	}
	notebook, createErr := nbpg.NewNotebookRepository(pool).Create(ctx, nbD.Notebook{OwnerID: owner, Name: "test", Document: nbD.Document{NBFormat: 4, NBFormatMinor: 5}, Cells: cells})
	if createErr != nil {
		t.Fatal(createErr)
	}
	nb = notebook.ID
	return New(pool), pool, owner, nb
}

// newSession создаёт готовую runtime-сессию с уникальным ID для теста хранилища.
func newSession(owner, nb int64) domain.Session {
	return domain.Session{ID: uuid.New(), OwnerID: owner, NotebookID: nb, RequestID: uuid.NewString(), Language: "python", State: domain.Ready, Stage: "ready", CreatedAt: time.Now(), LastActivityAt: time.Now()}
}

// TestConcurrentStartAndAdmission проверяет атомарный запуск сессии и отсутствие дублей при конкурентном
// принятии команды.
func TestConcurrentStartAndAdmission(t *testing.T) {
	s, _, owner, nb := integrationStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, e := s.Start(ctx, newSession(owner, nb)); results <- e })
	}
	wg.Wait()
	close(results)
	ok, conflict := 0, 0
	for e := range results {
		switch {
		case e == nil:
			ok++
		case errors.Is(e, domain.ErrActiveSessionExists):
			conflict++
		default:
			t.Fatal(e)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
	l, e := s.Claim(ctx, "worker")
	if e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256([]byte("print(42)"))
	r := domain.ExecuteRequest{ExecutionID: "run", CellID: "cell", SourceHash: hex.EncodeToString(h[:]), TimeoutSeconds: 300}
	x, e := s.Admit(ctx, owner, l.Session.ID, r)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.Admit(ctx, owner, l.Session.ID, r)
	if e != nil || again.ID != x.ID || again.Attempt != 1 {
		t.Fatal(again, e)
	}
	r.SourceHash = hex.EncodeToString(make([]byte, 32))
	if _, e = s.Admit(ctx, owner, l.Session.ID, r); !errors.Is(e, domain.ErrConflict) {
		t.Fatal(e)
	}
	if e = s.CheckLease(ctx, l); !errors.Is(e, domain.ErrLeaseLost) {
		t.Fatal("admission did not fence stale worker", e)
	}
	l, e = s.Claim(ctx, "other")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Mutate(ctx, owner, l.Session.ID, func(v *domain.Session) error { v.State = domain.Stopping; return nil }); e != nil {
		t.Fatal(e)
	}
	if e = s.Save(ctx, l, l.Session); !errors.Is(e, domain.ErrLeaseLost) {
		t.Fatal("stale worker overwrote stop", e)
	}
}

// TestEventTransactionRollsBackCursorAndOutput проверяет совместный откат курсора и результата при ошибке
// транзакции архива.
func TestEventTransactionRollsBackCursorAndOutput(t *testing.T) {
	s, p, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, e := s.Start(ctx, newSession(owner, nb))
	if e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256([]byte("print(42)"))
	x, e := s.Admit(ctx, owner, v.ID, domain.ExecuteRequest{ExecutionID: "run", CellID: "cell", SourceHash: hex.EncodeToString(h[:]), TimeoutSeconds: 300})
	if e != nil {
		t.Fatal(e)
	}
	l, e := s.Claim(ctx, "worker")
	if e != nil {
		t.Fatal(e)
	}
	bad := domain.AgentEvent{Seq: 1, SessionID: v.ID, ExecutionID: x.ID, Kind: "kernel_message", MsgType: "stream", Content: json.RawMessage(`42`)}
	if e = s.CommitEvents(ctx, l, []domain.AgentEvent{bad}); e == nil {
		t.Fatal("invalid content accepted")
	}
	v, e = s.Owned(ctx, owner, v.ID)
	if e != nil || v.AgentSeq != 0 {
		t.Fatal(v, e)
	}
	var count int
	_ = p.QueryRow(ctx, "SELECT count(*) FROM runtime_events").Scan(&count)
	if count != 0 {
		t.Fatal("event committed before failed reducer")
	}
	good := bad
	good.Content = json.RawMessage(`{"name":"stdout","text":"42"}`)
	if e = s.CommitEvents(ctx, l, []domain.AgentEvent{good}); e != nil {
		t.Fatal(e)
	}
	if e = s.CommitEvents(ctx, l, []domain.AgentEvent{good}); e != nil {
		t.Fatal(e)
	}
	x, e = s.Execution(ctx, owner, v.ID, x.ID)
	if e != nil || len(x.Outputs) != 1 {
		t.Fatal(x, e)
	}
	v, e = s.Owned(ctx, owner, v.ID)
	if e != nil || v.AgentSeq != 1 {
		t.Fatal(v, e)
	}
}

// TestAdmissionPersistsActivityForIdleProtection проверяет сохранение пользовательской активности при
// принятии команды.
func TestAdmissionPersistsActivityForIdleProtection(t *testing.T) {
	s, _, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, e := s.Start(ctx, newSession(owner, nb))
	if e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256([]byte("print(42)"))
	_, e = s.Admit(ctx, owner, v.ID, domain.ExecuteRequest{ExecutionID: "run", CellID: "cell", SourceHash: hex.EncodeToString(h[:]), TimeoutSeconds: 300})
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.Owned(ctx, owner, v.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !got.ActivityPending {
		t.Fatal("accepted command did not persist activity intent")
	}
}

// TestOversizedWireCommandRejectedBeforeAdmission проверяет отклонение слишком большой команды до её
// сохранения и отправки агенту.
func TestOversizedWireCommandRejectedBeforeAdmission(t *testing.T) {
	s, p, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, e := s.Start(ctx, newSession(owner, nb))
	if e != nil {
		t.Fatal(e)
	}
	code := strings.Repeat("😀", 600000)
	// Намеренно портим одноразовую фикстуру, чтобы проверить защитный admission.
	if _, e = p.Exec(ctx, `ALTER TABLE notebook_cells DROP CONSTRAINT notebook_cells_source_check`); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Exec(ctx, `UPDATE notebook_cells SET source=$2 WHERE notebook_id=$1 AND cell_id='cell'`, nb, code); e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256([]byte(code))
	_, e = s.Admit(ctx, owner, v.ID, domain.ExecuteRequest{ExecutionID: "run", CellID: "cell", SourceHash: hex.EncodeToString(h[:]), TimeoutSeconds: 300})
	if !errors.Is(e, domain.ErrInvalid) {
		t.Fatal(e)
	}
	var count int
	_ = p.QueryRow(ctx, "SELECT count(*) FROM runtime_executions").Scan(&count)
	if count != 0 {
		t.Fatal("oversized immutable command persisted")
	}
}

// TestWorkerSavePreservesEventGeneration проверяет, что сохранение шага обработчика не затирает поколение,
// обновлённое событиями.
func TestWorkerSavePreservesEventGeneration(t *testing.T) {
	s, _, owner, nb := integrationStore(t)
	ctx := context.Background()
	v := newSession(owner, nb)
	v.Generation = "old"
	v, e := s.Start(ctx, v)
	if e != nil {
		t.Fatal(e)
	}
	l, e := s.Claim(ctx, "worker")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.CommitEvents(ctx, l, []domain.AgentEvent{{Seq: 1, SessionID: v.ID, Kind: "kernel_started", Generation: "new"}}); e != nil {
		t.Fatal(e)
	}
	if e = s.Save(ctx, l, l.Session); e != nil {
		t.Fatal(e)
	}
	v, e = s.Owned(ctx, owner, v.ID)
	if e != nil || v.Generation != "new" {
		t.Fatal(v.Generation, e)
	}
}
