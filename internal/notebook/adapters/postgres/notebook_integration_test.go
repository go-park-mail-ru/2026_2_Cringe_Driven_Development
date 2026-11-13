package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"

	notebookcursor "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/cursor"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/ipynb"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testRepository(t *testing.T) (*NotebookRepository, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "notebook_" + uuid.NewString()
	q := pgx.Identifier{schema}.Sanitize()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+q); e != nil {
		t.Fatal(e)
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	if path := os.Getenv("NOTEBOOK_SQL_TRACE"); path != "" {
		file, traceErr := os.Create(path)
		if traceErr != nil {
			t.Fatal(traceErr)
		}
		t.Cleanup(func() { _ = file.Close() })
		cfg.ConnConfig.Tracer = &sqlTrace{file: file}
	}
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { pool.Close(); _, _ = admin.Exec(ctx, "DROP SCHEMA "+q+" CASCADE"); admin.Close() })
	if e = migrations.Up(ctx, pool); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO users(id,login,password_hash) OVERRIDING SYSTEM VALUE VALUES(1,'owner','hash')`); e != nil {
		t.Fatal(e)
	}
	return NewNotebookRepository(pool), pool
}
func TestNotebookTransactionsAndResults(t *testing.T) {
	repo, _ := testRepository(t)
	ctx := context.Background()
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, notebookcursor.New([]byte("list-test-key")))
	n, e := uc.Create(ctx, 1, " notebook ")
	if e != nil {
		t.Fatal(e)
	}
	id := n.Cells[0].ID
	s, e := uc.Content(ctx, n.ID, 1)
	if e != nil {
		t.Fatal(e)
	}
	edited, e := uc.EditCell(ctx, n.ID, 1, s.Hash, id, "  я😀\nsecond")
	if e != nil {
		t.Fatal(e)
	}
	src, e := repo.ExecutionSource(ctx, n.ID, 1, id)
	if e != nil {
		t.Fatal(e)
	}
	count := int64(3)
	result := usecase.CellResult{CellID: id, SourceVersion: src.SourceVersion, Outputs: json.RawMessage(`[{"text":"done"}]`), ExecutionCount: &count}
	rev, e := repo.SaveResult(ctx, n.ID, 1, result)
	if e != nil {
		t.Fatal(e)
	}
	added, e := uc.AddCell(ctx, n.ID, 1, rev, domain.CellKindMarkdown, "text", nil)
	if e != nil {
		t.Fatal(e)
	}
	moved, e := uc.MoveCell(ctx, n.ID, 1, added.Hash, id, 1)
	if e != nil {
		t.Fatal(e)
	}
	got, e := repo.Get(ctx, n.ID, 1)
	if e != nil {
		t.Fatal(e)
	}
	if got.Revision != moved || string(got.Cells[1].Outputs) != `[{"text":"done"}]` || *got.Cells[1].ExecutionCount != 3 {
		t.Fatalf("lost result: %+v", got)
	}
	before := got.UpdatedAt
	noop, e := uc.EditCell(ctx, n.ID, 1, moved, id, "  я😀\nsecond")
	if e != nil {
		t.Fatal(e)
	}
	got, _ = repo.Get(ctx, n.ID, 1)
	if noop.Hash != moved || !got.UpdatedAt.Equal(before) {
		t.Fatal("no-op changed revision/date")
	}
	// У двух записей в одну ревизию ноутбука ровно один победитель.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, s := range []string{"new1", "new2"} {
		wg.Add(1)
		go func() { defer wg.Done(); _, writeErr := uc.EditCell(ctx, n.ID, 1, moved, id, s); errs <- writeErr }()
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for e := range errs {
		switch {
		case e == nil:
			success++
		case errors.Is(e, usecase.ErrNotebookConflict):
			conflict++
		default:
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent writes were not serialized")
	}
	if _, e = repo.SaveResult(ctx, n.ID, 1, result); !errors.Is(e, usecase.ErrStaleSource) {
		t.Fatalf("stale result: %v", e)
	}
	got, e = repo.Get(ctx, n.ID, 1)
	if e != nil {
		t.Fatal(e)
	}
	if string(got.Cells[1].Outputs) != `[]` || got.Cells[1].ExecutionCount != nil {
		t.Fatal("source did not clear results")
	}
	raw, e := ipynb.EncodeDocument(got.Document, got.Cells)
	if e != nil {
		t.Fatal(e)
	}
	if int64(len(raw)) != got.SizeBytes {
		t.Fatalf("accounting: %d != %d", len(raw), got.SizeBytes)
	}
	if _, e = repo.Get(ctx, n.ID, 2); !errors.Is(e, usecase.ErrNotebookNotFound) {
		t.Fatal("owner isolation")
	}
	_ = edited
}

func TestPreviewAndExactSizeAcrossOperations(t *testing.T) {
	repo, _ := testRepository(t)
	ctx := context.Background()
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, notebookcursor.New([]byte("list-test-key")))
	n, err := uc.Create(ctx, 1, "preview")
	if err != nil {
		t.Fatal(err)
	}
	check := func(want string) domain.Notebook {
		t.Helper()
		got, e := repo.Get(ctx, n.ID, 1)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := ipynb.EncodeDocument(got.Document, got.Cells)
		if e != nil {
			t.Fatal(e)
		}
		list, e := uc.List(ctx, 1, usecase.ListInput{})
		if e != nil {
			t.Fatal(e)
		}
		if got.PreviewCode != want || list.Items[0].PreviewCode != want || int64(len(raw)) != got.SizeBytes {
			t.Fatalf("preview=%q want=%q bytes=%d accounted=%d", got.PreviewCode, want, len(raw), got.SizeBytes)
		}
		return got
	}
	n = check("")
	r, e := uc.EditCell(ctx, n.ID, 1, n.Revision, n.Cells[0].ID, "\t\n\u2003")
	if e != nil {
		t.Fatal(e)
	}
	md, e := uc.AddCell(ctx, n.ID, 1, r.Hash, domain.CellKindMarkdown, "ignored", nil)
	if e != nil {
		t.Fatal(e)
	}
	a, e := uc.AddCell(ctx, n.ID, 1, md.Hash, domain.CellKindCode, "  я😀\n2\n3\n4\n5\n6\n7", nil)
	if e != nil {
		t.Fatal(e)
	}
	n = check("  я😀\n2\n3\n4\n5\n6")
	b, e := uc.AddCell(ctx, n.ID, 1, a.Hash, domain.CellKindCode, "\"\\\nnext", nil)
	if e != nil {
		t.Fatal(e)
	}
	rev, e := uc.MoveCell(ctx, n.ID, 1, b.Hash, b.Cell.ID, 0)
	if e != nil {
		t.Fatal(e)
	}
	n = check("\"\\\nnext")
	if _, e = uc.EditCell(ctx, n.ID, 1, a.Hash, b.Cell.ID, "rejected"); !errors.Is(e, usecase.ErrNotebookConflict) {
		t.Fatal(e)
	}
	check("\"\\\nnext")
	rev, e = uc.RemoveCell(ctx, n.ID, 1, rev, b.Cell.ID)
	if e != nil {
		t.Fatal(e)
	}
	check("  я😀\n2\n3\n4\n5\n6")
	if _, e = repo.Apply(ctx, n.ID, 1, a.Hash, usecase.Operation{Kind: usecase.Delete, Index: ptr(-1)}); !errors.Is(e, usecase.ErrNotebookConflict) {
		t.Fatalf("index before revision: %v", e)
	}
	if e = uc.DeleteCell(ctx, n.ID, 1, rev, 2); e != nil {
		t.Fatal(e)
	}
	check("")
}
func ptr(v int) *int { return &v }

func TestResultFollowsCellAndRejectsDeletedSource(t *testing.T) {
	repo, _ := testRepository(t)
	ctx := context.Background()
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, notebookcursor.New([]byte("list-test-key")))
	n, e := uc.Create(ctx, 1, "results")
	if e != nil {
		t.Fatal(e)
	}
	id := n.Cells[0].ID
	src, e := repo.ExecutionSource(ctx, n.ID, 1, id)
	if e != nil {
		t.Fatal(e)
	}
	a, e := uc.AddCell(ctx, n.ID, 1, n.Revision, domain.CellKindCode, "other", nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = uc.MoveCell(ctx, n.ID, 1, a.Hash, id, 1); e != nil {
		t.Fatal(e)
	}
	result := usecase.CellResult{CellID: id, SourceVersion: src.SourceVersion, Outputs: json.RawMessage(`[{"text":"ok"}]`), OutputTruncated: true}
	rev, e := repo.SaveResult(ctx, n.ID, 1, result)
	if e != nil {
		t.Fatal(e)
	}
	got, e := repo.Get(ctx, n.ID, 1)
	if e != nil {
		t.Fatal(e)
	}
	if !got.Cells[1].OutputTruncated {
		t.Fatal("lost truncation flag")
	}
	noop, e := repo.SaveResult(ctx, n.ID, 1, result)
	if e != nil || noop != rev {
		t.Fatal("result no-op changed revision")
	}
	changed, e := uc.EditCell(ctx, n.ID, 1, rev, id, "changed")
	if e != nil {
		t.Fatal(e)
	}
	changed, e = uc.EditCell(ctx, n.ID, 1, changed.Hash, id, src.Source)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.SaveResult(ctx, n.ID, 1, result); !errors.Is(e, usecase.ErrStaleSource) {
		t.Fatalf("ABA source accepted: %v", e)
	}
	if _, e = uc.RemoveCell(ctx, n.ID, 1, changed.Hash, id); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.SaveResult(ctx, n.ID, 1, result); !errors.Is(e, usecase.ErrStaleSource) {
		t.Fatalf("deleted source accepted: %v", e)
	}
}

// Хук после SELECT метаданных вызывает коммит между этим SELECT и SELECT ячеек,
// доказывая согласованность repeatable read, а не полагаясь на тайминги.
type snapshotDB struct {
	DB
	after func()
}

func (d snapshotDB) Begin(ctx context.Context) (pgx.Tx, error) { //nolint:ireturn // Реализует границу транзакции pgx для детерминированного теста снимка.
	tx, err := d.DB.Begin(ctx)
	return snapshotTx{Tx: tx, after: d.after}, err
}

type snapshotTx struct {
	pgx.Tx
	after func()
}

func (tx snapshotTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row { //nolint:ireturn // Реализует pgx.Tx, вставляя коммит после сканирования строки.
	row := tx.Tx.QueryRow(ctx, sql, args...)
	if sql == selectNotebook {
		return snapshotRow{Row: row, after: tx.after}
	}
	return row
}

type snapshotRow struct {
	pgx.Row
	after func()
}

func (row snapshotRow) Scan(dest ...any) error {
	err := row.Row.Scan(dest...)
	if err == nil {
		row.after()
	}
	return err
}
func TestContentSnapshotDuringCommittedSave(t *testing.T) {
	repo, pool := testRepository(t)
	ctx := context.Background()
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, notebookcursor.New([]byte("list-test-key")))
	n, err := uc.Create(ctx, 1, "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	reader := NewNotebookRepository(snapshotDB{DB: pool, after: func() {
		if _, writeErr := uc.EditCell(ctx, n.ID, 1, n.Revision, n.Cells[0].ID, "after"); writeErr != nil {
			t.Fatal(writeErr)
		}
	}})
	got, err := reader.Get(ctx, n.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != n.Revision || got.Cells[0].Source != "" {
		t.Fatalf("mixed snapshot: %+v", got)
	}
	current, err := repo.Get(ctx, n.ID, 1)
	if err != nil || current.Cells[0].Source != "after" || current.Revision == n.Revision {
		t.Fatal("writer did not commit")
	}
}
func TestPublishFailureRollsBackCellsAndMetadata(t *testing.T) {
	repo, pool := testRepository(t)
	ctx := context.Background()
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, notebookcursor.New([]byte("list-test-key")))
	n, err := uc.Create(ctx, 1, "rollback")
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_publish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected publish failure'; END $$; CREATE TRIGGER fail_publish BEFORE UPDATE ON notebooks FOR EACH ROW EXECUTE FUNCTION fail_publish()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = uc.EditCell(ctx, n.ID, 1, n.Revision, n.Cells[0].ID, "must rollback"); err == nil {
		t.Fatal("expected failure")
	}
	got, err := repo.Get(ctx, n.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != n.Revision || got.Cells[0].Source != "" || got.SizeBytes != n.SizeBytes || got.PreviewCode != "" || !got.UpdatedAt.Equal(n.UpdatedAt) {
		t.Fatalf("partial transaction: %+v", got)
	}
}

type activeNotebook struct {
	notebookID int64
	calls      int
}

func (a *activeNotebook) ActiveNotebook(context.Context, int64) (int64, error) {
	a.calls++
	return a.notebookID, nil
}
func TestSessionBadgeSurvivesResultsAndEndsWithSession(t *testing.T) {
	repo, _ := testRepository(t)
	ctx := context.Background()
	active := &activeNotebook{}
	uc := usecase.NewNotebookUsecase(repo, active, notebookcursor.New([]byte("list-test-key")))
	n, err := uc.Create(ctx, 1, "active")
	if err != nil {
		t.Fatal(err)
	}
	other, err := uc.Create(ctx, 1, "other")
	if err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		before := active.calls
		list, e := uc.List(ctx, 1, usecase.ListInput{})
		if e != nil {
			t.Fatal(e)
		}
		if active.calls != before+1 {
			t.Fatal("not one runtime lookup")
		}
		for _, item := range list.Items {
			expected := item.ID == n.ID && want
			if item.HasActiveSession != expected {
				t.Fatalf("badge: %+v", item)
			}
		}
	}
	check(false)
	active.notebookID = n.ID
	check(true)
	src, err := repo.ExecutionSource(ctx, n.ID, 1, n.Cells[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{`[{"output_type":"stream","text":"ok"}]`, `[{"output_type":"error","ename":"Error","evalue":"failed","traceback":[]}]`} {
		if _, err = repo.SaveResult(ctx, n.ID, 1, usecase.CellResult{CellID: src.CellID, SourceVersion: src.SourceVersion, Outputs: json.RawMessage(output)}); err != nil {
			t.Fatal(err)
		}
		check(true)
	}
	active.notebookID = 0
	check(false)
	active.notebookID = other.ID
	list, err := uc.List(ctx, 1, usecase.ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Items {
		if item.HasActiveSession != (item.ID == other.ID) {
			t.Fatal("badge belongs to wrong notebook")
		}
	}
}

func TestRuntimeResultRacesWithCellOperations(t *testing.T) {
	for _, kind := range []usecase.OperationKind{usecase.Edit, usecase.Move, usecase.Delete} {
		t.Run(string(kind), func(t *testing.T) {
			repo, _ := testRepository(t)
			ctx := context.Background()
			uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, notebookcursor.New([]byte("list-test-key")))
			n, err := uc.Create(ctx, 1, "race")
			if err != nil {
				t.Fatal(err)
			}
			id := n.Cells[0].ID
			first, err := uc.EditCell(ctx, n.ID, 1, n.Revision, id, "initial")
			if err != nil {
				t.Fatal(err)
			}
			second, err := uc.AddCell(ctx, n.ID, 1, first.Hash, domain.CellKindCode, "other", nil)
			if err != nil {
				t.Fatal(err)
			}
			src, err := repo.ExecutionSource(ctx, n.ID, 1, id)
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			saved, changed := make(chan error, 1), make(chan error, 1)
			go func() {
				<-start
				_, saveErr := repo.SaveResult(ctx, n.ID, 1, usecase.CellResult{CellID: id, SourceVersion: src.SourceVersion, Outputs: json.RawMessage(`[{"text":"initial result"}]`)})
				saved <- saveErr
			}()
			go func() {
				<-start
				_, changeErr := repo.Apply(ctx, n.ID, 1, second.Hash, usecase.Operation{Kind: kind, CellID: id, Source: "changed", Index: ptr(1)})
				changed <- changeErr
			}()
			close(start)
			saveErr, changeErr := <-saved, <-changed
			if changeErr != nil {
				t.Fatal(changeErr)
			}
			if saveErr != nil && !errors.Is(saveErr, usecase.ErrStaleSource) {
				t.Fatal(saveErr)
			}
			if kind == usecase.Move && saveErr != nil {
				t.Fatalf("move rejected result for unchanged source: %v", saveErr)
			}
			got, err := repo.Get(ctx, n.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			for _, cell := range got.Cells {
				if cell.ID == id && cell.Source == "changed" && string(cell.Outputs) != "[]" {
					t.Fatal("old result contaminated changed source")
				}
			}
		})
	}
}
func TestResultOverwritePreservesAttachments(t *testing.T) {
	repo, _ := testRepository(t)
	ctx := context.Background()
	n, err := repo.Create(ctx, domain.Notebook{OwnerID: 1, Name: "attachments", Document: domain.Document{NBFormat: 4, NBFormatMinor: 5}, Cells: []domain.Cell{{ID: "a", Kind: domain.CellKindCode, Source: "source", Outputs: json.RawMessage(`[]`), HasExecutionCount: true, Attachments: json.RawMessage(`{"keep":{"image/png":"abc"}}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	src, err := repo.ExecutionSource(ctx, n.ID, 1, "a")
	if err != nil {
		t.Fatal(err)
	}
	for _, outputs := range []string{`[{"text":"one"}]`, `[{"text":"two"}]`} {
		if _, err = repo.SaveResult(ctx, n.ID, 1, usecase.CellResult{CellID: "a", SourceVersion: src.SourceVersion, Outputs: json.RawMessage(outputs)}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.Get(ctx, n.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Cells[0].Attachments) != `{"keep":{"image/png":"abc"}}` {
		t.Fatal("lost attachments")
	}
	for _, bad := range []string{`[42]`, `[null]`} {
		if _, err = repo.SaveResult(ctx, n.ID, 1, usecase.CellResult{CellID: "a", SourceVersion: src.SourceVersion, Outputs: json.RawMessage(bad)}); !errors.Is(err, usecase.ErrInvalidDocument) {
			t.Fatalf("accepted invalid result: %v", err)
		}
	}
	after, err := repo.Get(ctx, n.ID, 1)
	if err != nil || after.Revision != got.Revision || string(after.Cells[0].Outputs) != string(got.Cells[0].Outputs) {
		t.Fatal("invalid result changed current data")
	}
}
