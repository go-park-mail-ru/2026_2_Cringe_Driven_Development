package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	notebookcursor "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/cursor"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/jackc/pgx/v5/pgxpool"
)

func renameNotebook(t *testing.T, repo *NotebookRepository, pool *pgxpool.Pool) domain.Notebook {
	t.Helper()
	count := int64(3)
	n, err := repo.Create(context.Background(), domain.Notebook{
		OwnerID: 1, Name: "original",
		Document: domain.Document{NBFormat: 4, NBFormatMinor: 5, Metadata: json.RawMessage(`{"custom":"document"}`), Extra: map[string]json.RawMessage{"custom": json.RawMessage(`true`)}},
		Cells:    []domain.Cell{{ID: "code", Kind: domain.CellKindCode, Source: "print(42)", Metadata: json.RawMessage(`{"custom":"cell"}`), Extra: map[string]json.RawMessage{"custom": json.RawMessage(`42`)}, Outputs: json.RawMessage(`[{"output_type":"stream","name":"stdout","text":"kept"}]`), ExecutionCount: &count, HasExecutionCount: true, HasRuntimeMetadata: true, OutputTruncated: true, Attachments: json.RawMessage(`{"image":{"image/png":"aGVsbG8="}}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), `UPDATE notebooks SET updated_at='2026-01-01T00:00:00Z' WHERE id=$1`, n.ID); err != nil {
		t.Fatal(err)
	}
	n, err = repo.Get(context.Background(), n.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRenameNotebookPersistsOnlyName(t *testing.T) {
	repo, pool := testRepository(t)
	before := renameNotebook(t, repo, pool)
	got, err := repo.Rename(context.Background(), before.ID, 1, "Новое название")
	if err != nil {
		t.Fatal(err)
	}
	want := before
	want.Name, want.UpdatedAt = "Новое название", got.UpdatedAt
	if !reflect.DeepEqual(got, want) || !got.UpdatedAt.After(before.UpdatedAt) {
		t.Fatalf("rename changed content or missed timestamp: %+v", got)
	}
	saved, err := repo.Get(context.Background(), before.ID, 1)
	if err != nil || !reflect.DeepEqual(saved, got) {
		t.Fatalf("not persisted: %+v %v", saved, err)
	}
	list, err := repo.ListPage(context.Background(), 1, usecase.ListQuery{Limit: 24})
	if err != nil || len(list.Items) != 1 || list.Items[0].Name != "Новое название" || !list.Items[0].UpdatedAt.Equal(got.UpdatedAt) {
		t.Fatalf("list: %+v %v", list, err)
	}
}

func TestRenameNotebookNoop(t *testing.T) {
	repo, pool := testRepository(t)
	before := renameNotebook(t, repo, pool)
	if _, err := pool.Exec(context.Background(), `CREATE FUNCTION reject_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'unexpected update'; END $$; CREATE TRIGGER reject_update BEFORE UPDATE ON notebooks FOR EACH ROW EXECUTE FUNCTION reject_update()`); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Rename(context.Background(), before.ID, 1, before.Name)
	if err != nil || !reflect.DeepEqual(got, before) {
		t.Fatalf("same name must not write: %+v %v", got, err)
	}
}

func TestRenameNotebookOwnerAndMissing(t *testing.T) {
	repo, pool := testRepository(t)
	before := renameNotebook(t, repo, pool)
	if _, err := pool.Exec(context.Background(), `INSERT INTO users(id,login,password_hash) OVERRIDING SYSTEM VALUE VALUES(2,'other','hash')`); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]int64{{before.ID, 2}, {before.ID + 1, 1}} {
		got, err := repo.Rename(context.Background(), pair[0], pair[1], "hidden")
		if !errors.Is(err, usecase.ErrNotebookNotFound) || !reflect.DeepEqual(got, domain.Notebook{}) {
			t.Fatalf("expected hidden/missing notebook: %+v %v", got, err)
		}
	}
	got, err := repo.Get(context.Background(), before.ID, 1)
	if err != nil || !reflect.DeepEqual(got, before) {
		t.Fatal("foreign rename changed data", err)
	}
	if err = repo.DeleteNotebook(context.Background(), before.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Rename(context.Background(), before.ID, 1, "deleted"); !errors.Is(err, usecase.ErrNotebookNotFound) {
		t.Fatal(err)
	}
}

func TestRenameNotebookDuplicateNames(t *testing.T) {
	repo, _ := testRepository(t)
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, notebookcursor.New([]byte("list-test-key")))
	for range 2 {
		n, err := uc.Create(context.Background(), 1, "old")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = repo.Rename(context.Background(), n.ID, 1, "same"); err != nil {
			t.Fatal(err)
		}
	}
	list, err := repo.ListPage(context.Background(), 1, usecase.ListQuery{Limit: 24})
	if err != nil || len(list.Items) != 2 || list.Items[0].Name != "same" || list.Items[1].Name != "same" {
		t.Fatalf("duplicates: %+v %v", list, err)
	}
}

func TestRenameNotebookEmptyCells(t *testing.T) {
	repo, pool := testRepository(t)
	n := renameNotebook(t, repo, pool)
	if _, err := repo.Apply(context.Background(), n.ID, 1, n.Revision, usecase.Operation{Kind: usecase.Delete, CellID: "code"}); err != nil {
		t.Fatal(err)
	}
	before, err := repo.Get(context.Background(), n.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.Rename(context.Background(), n.ID, 1, "empty")
	if err != nil || got.Cells == nil || len(got.Cells) != 0 || got.Revision != before.Revision {
		t.Fatalf("empty notebook: %+v %v", got, err)
	}
}

func TestRenameNotebookRollback(t *testing.T) {
	repo, pool := testRepository(t)
	before := renameNotebook(t, repo, pool)
	if _, err := pool.Exec(context.Background(), `CREATE FUNCTION reject_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected rename failure'; END $$; CREATE TRIGGER reject_update BEFORE UPDATE ON notebooks FOR EACH ROW EXECUTE FUNCTION reject_update()`); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Rename(context.Background(), before.ID, 1, "must rollback")
	if err == nil || !reflect.DeepEqual(got, domain.Notebook{}) {
		t.Fatalf("failure returned success: %+v %v", got, err)
	}
	saved, err := repo.Get(context.Background(), before.ID, 1)
	if err != nil || !reflect.DeepEqual(saved, before) {
		t.Fatal("partial rename", err)
	}
}
