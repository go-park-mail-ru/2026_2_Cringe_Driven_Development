package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
)

func TestSharingReadMatrixAndAtomicReplacement(t *testing.T) {
	repo, pool := testRepository(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,login,password_hash) OVERRIDING SYSTEM VALUE VALUES(2,'Reader','hash'),(3,'Other','hash')`); err != nil {
		t.Fatal(err)
	}
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, nil)
	n, err := uc.Create(ctx, 1, "matrix")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []domain.SharingMode{domain.SharingModePrivate, domain.SharingModeRestricted, domain.SharingModePublic} {
		logins := []string{}
		if mode == domain.SharingModeRestricted {
			logins = []string{"reader", "READER", "owner"}
		}
		settings, putErr := uc.PutSharing(ctx, n.ID, 1, mode, logins)
		if putErr != nil {
			t.Fatal(putErr)
		}
		wantReaders := 0
		if mode == domain.SharingModeRestricted {
			wantReaders = 1
		}
		if len(settings.Readers) != wantReaders {
			t.Fatalf("dedup/owner exclusion: %+v", settings)
		}
		for _, user := range []int64{0, 1, 2, 3} {
			want := user == 1 || mode == domain.SharingModePublic || (mode == domain.SharingModeRestricted && user == 2)
			got, e := uc.Get(ctx, n.ID, user)
			if want {
				if e != nil {
					t.Fatalf("mode %s user %d: %v", mode, user, e)
				}
				if got.Access.IsOwner != (user == 1) || got.Access.CanEdit != (user == 1) || got.Access.CanManageSharing != (user == 1) || got.Access.CanClone != (user != 0) {
					t.Fatalf("wrong access: %+v", got.Access)
				}
			} else if !errors.Is(e, usecase.ErrNotebookNotFound) {
				t.Fatalf("mode %s user %d: %v", mode, user, e)
			}
			_, e = uc.Content(ctx, n.ID, user)
			if (e == nil) != want {
				t.Fatalf("content mode %s user %d: %v", mode, user, e)
			}
			if e = uc.CheckRead(ctx, n.ID, user); (e == nil) != want {
				t.Fatalf("permission check mode %s user %d: %v", mode, user, e)
			}
			if user != 1 {
				if _, e = repo.Apply(ctx, n.ID, user, n.Revision, usecase.Operation{Kind: usecase.Edit, CellID: n.Cells[0].ID, Source: "forbidden"}); !errors.Is(e, usecase.ErrNotebookNotFound) {
					t.Fatal("editing allowed", e)
				}
				if _, e = repo.GetSharing(ctx, n.ID, user); !errors.Is(e, usecase.ErrNotebookNotFound) {
					t.Fatal("settings leaked", e)
				}
				if _, e = repo.Rename(ctx, n.ID, user, "forbidden"); !errors.Is(e, usecase.ErrNotebookNotFound) {
					t.Fatal("rename allowed", e)
				}
				if e = repo.DeleteNotebook(ctx, n.ID, user); !errors.Is(e, usecase.ErrNotebookNotFound) {
					t.Fatal("delete allowed", e)
				}
			}
		}
	}
	before, err := uc.PutSharing(ctx, n.ID, 1, domain.SharingModeRestricted, []string{"Reader"})
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := uc.PutSharing(ctx, n.ID, 1, domain.SharingModeRestricted, []string{"Reader", "READER"})
	if err != nil || !reflect.DeepEqual(before, repeated) {
		t.Fatalf("non-idempotent PUT: %+v %v", repeated, err)
	}
	if _, err = uc.PutSharing(ctx, n.ID, 1, domain.SharingModeRestricted, []string{"Other", "missing"}); !errors.Is(err, usecase.ErrInvalidSharing) {
		t.Fatal(err)
	}
	after, err := uc.GetSharing(ctx, n.ID, 1)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("partial replacement: %+v %v", after, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE users SET login='Renamed' WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	after, err = uc.GetSharing(ctx, n.ID, 1)
	if err != nil || after.Readers[0].Login != "Renamed" {
		t.Fatalf("stale login: %+v %v", after, err)
	}
	if _, err = uc.Get(ctx, n.ID, 2); err != nil {
		t.Fatal("rename lost access", err)
	}
	if _, err = uc.PutSharing(ctx, n.ID, 1, domain.SharingModePrivate, []string{}); err != nil {
		t.Fatal(err)
	}
	if _, err = uc.Get(ctx, n.ID, 2); !errors.Is(err, usecase.ErrNotebookNotFound) {
		t.Fatal("revoked access allowed", err)
	}
}

func TestCloneCompleteIndependentDocument(t *testing.T) {
	repo, pool := testRepository(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,login,password_hash) OVERRIDING SYSTEM VALUE VALUES(2,'reader','hash')`); err != nil {
		t.Fatal(err)
	}
	count := int64(9)
	src, err := repo.Create(ctx, domain.Notebook{OwnerID: 1, Name: "source", Document: domain.Document{NBFormat: 4, NBFormatMinor: 5, Metadata: json.RawMessage(`{"custom":"header"}`), Extra: map[string]json.RawMessage{"extra_field": json.RawMessage(`{"keep":true}`)}}, Cells: []domain.Cell{
		{ID: "code", Kind: domain.CellKindCode, Source: "print(1)", Metadata: json.RawMessage(`{"custom":1}`), Extra: map[string]json.RawMessage{"x": json.RawMessage(`true`)}, Outputs: json.RawMessage(`[{"output_type":"stream","name":"stdout","text":"saved"}]`), ExecutionCount: &count, HasExecutionCount: true, OutputTruncated: true, HasRuntimeMetadata: true},
		{ID: "markdown", Kind: domain.CellKindMarkdown, Source: "![a](attachment:a.png)", Metadata: json.RawMessage(`{}`), Attachments: json.RawMessage(`{"a.png":{"image/png":"YWJj"}}`)},
		{ID: "raw", Kind: "raw", Source: "raw text", Metadata: json.RawMessage(`{"format":"text/plain"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, nil)
	if _, err = uc.PutSharing(ctx, src.ID, 1, domain.SharingModeRestricted, []string{"reader"}); err != nil {
		t.Fatal(err)
	}
	clone, err := uc.Clone(ctx, src.ID, 2, "  копия 😀  ")
	if err != nil {
		t.Fatal(err)
	}
	if clone.ID == src.ID || clone.OwnerID != 2 || clone.Name != "копия 😀" || clone.Revision == src.Revision || clone.SharingMode != domain.SharingModePrivate {
		t.Fatalf("clone identity: %+v", clone)
	}
	got, err := repo.Get(ctx, clone.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	original, err := repo.Get(ctx, src.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Document, original.Document) || len(got.Cells) != len(original.Cells) {
		t.Fatal("document not preserved")
	}
	for i, c := range got.Cells {
		old := original.Cells[i]
		if c.SourceVersion == old.SourceVersion {
			t.Fatal("source version reused")
		}
		c.ID = old.ID
		c.SourceVersion = old.SourceVersion
		if !reflect.DeepEqual(c, old) {
			t.Fatalf("cell %d differs:\ngot %+v\nwant %+v", i, c, old)
		}
	}
	settings, err := uc.GetSharing(ctx, clone.ID, 2)
	if err != nil || len(settings.Readers) != 0 {
		t.Fatal(settings, err)
	}
	page, err := uc.List(ctx, 2, usecase.ListInput{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != clone.ID {
		t.Fatalf("copy missing from own list: %+v %v", page, err)
	}
	if _, err = uc.EditCell(ctx, clone.ID, 2, clone.Revision, clone.Cells[0].ID, "independent"); err != nil {
		t.Fatal(err)
	}
	unchanged, err := repo.Get(ctx, src.ID, 1)
	if err != nil || unchanged.Cells[0].Source != original.Cells[0].Source {
		t.Fatal("editing copy changed source", err)
	}
	if err = repo.DeleteNotebook(ctx, src.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = uc.Get(ctx, clone.ID, 2); err != nil {
		t.Fatal("original deletion broke copy", err)
	}
	if _, err = uc.Clone(ctx, clone.ID, 0, "anonymous"); err == nil {
		t.Fatal("anonymous clone allowed")
	}
}

func TestSharingMigrationPreservesExistingNotebook(t *testing.T) {
	repo, pool := testRepository(t)
	ctx := t.Context()
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, nil)
	before, err := uc.Create(ctx, 1, "existing")
	if err != nil {
		t.Fatal(err)
	}
	// Восстанавливаем структуру непосредственно до новой миграции.
	if _, err = pool.Exec(ctx, `DROP TABLE notebook_readers; ALTER TABLE notebooks DROP COLUMN sharing_mode`); err != nil {
		t.Fatal(err)
	}
	sql, err := os.ReadFile("../../../../migrations/20261117225455_add_notebook_sharing.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(sql), "-- +goose Down")
	if _, err = pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	after, err := repo.Get(ctx, before.ID, 1)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("existing notebook changed: %+v %v", after, err)
	}
}

func TestCloneEmptyNotebook(t *testing.T) {
	repo, _ := testRepository(t)
	ctx := context.Background()
	src, err := repo.Create(ctx, domain.Notebook{OwnerID: 1, Name: "empty", Document: domain.Document{NBFormat: 4, NBFormatMinor: 5}})
	if err != nil {
		t.Fatal(err)
	}
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, nil)
	clone, err := uc.Clone(ctx, src.ID, 1, "empty copy")
	if err != nil || len(clone.Cells) != 0 {
		t.Fatal(clone, err)
	}
}
