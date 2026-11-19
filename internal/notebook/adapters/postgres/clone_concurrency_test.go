package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCloneSerializesWithSourceChanges(t *testing.T) {
	for _, operation := range []string{"revoke", "delete", "edit", "result"} {
		for _, cloneFirst := range []bool{true, false} {
			order := "change-first"
			if cloneFirst {
				order = "clone-first"
			}
			t.Run(operation+"/"+order, func(t *testing.T) {
				repo, pool := testRepository(t)
				ctx := t.Context()
				if _, err := pool.Exec(ctx, `INSERT INTO users(id,login,password_hash) OVERRIDING SYSTEM VALUE VALUES(2,'reader','hash')`); err != nil {
					t.Fatal(err)
				}
				uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, nil)
				source, err := uc.Create(ctx, 1, "original")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = repo.PutSharing(ctx, source.ID, 1, domain.SharingModeRestricted, []string{"reader"}); err != nil {
					t.Fatal(err)
				}
				var copy domain.Notebook
				clone := func(r *NotebookRepository) error { var e error; copy, e = r.Clone(ctx, source.ID, 2, "copy"); return e }
				change := func(r *NotebookRepository) error {
					switch operation {
					case "revoke":
						_, e := r.PutSharing(ctx, source.ID, 1, domain.SharingModePrivate, []string{})
						return e
					case "delete":
						return r.DeleteNotebook(ctx, source.ID, 1)
					case "edit":
						_, e := r.Apply(ctx, source.ID, 1, source.Revision, usecase.Operation{Kind: usecase.Edit, CellID: source.Cells[0].ID, Source: "changed"})
						return e
					default:
						_, e := r.SaveResult(ctx, source.ID, 1, usecase.CellResult{CellID: source.Cells[0].ID, SourceVersion: source.Cells[0].SourceVersion, Outputs: []byte(`[{"text":"changed"}]`)})
						return e
					}
				}
				first, second := clone, change
				if !cloneFirst {
					first, second = change, clone
				}
				err1, err2 := serializeRename(t, pool, first, second)
				if err1 != nil {
					t.Fatal(err1)
				}
				if !cloneFirst && (operation == "revoke" || operation == "delete") {
					if !errors.Is(err2, usecase.ErrNotebookNotFound) {
						t.Fatalf("copy after access loss: %v", err2)
					}
					var count int
					if e := pool.QueryRow(ctx, `SELECT count(*) FROM notebooks WHERE owner_id=2`).Scan(&count); e != nil || count != 0 {
						t.Fatalf("partial copy: %d %v", count, e)
					}
					return
				}
				if err2 != nil {
					t.Fatal(err2)
				}
				got, e := repo.Get(ctx, copy.ID, 2)
				if e != nil {
					t.Fatal(e)
				}
				wantSource := ""
				wantOutputs := "[]"
				if !cloneFirst && operation == "edit" {
					wantSource = "changed"
				}
				if !cloneFirst && operation == "result" {
					wantOutputs = `[{"text":"changed"}]`
				}
				if len(got.Cells) != 1 || got.Cells[0].Source != wantSource || string(got.Cells[0].Outputs) != wantOutputs {
					t.Fatalf("mixed/wrong snapshot: %+v", got.Cells)
				}
			})
		}
	}
}

type cloneFailureDB struct{ DB }

//nolint:ireturn // Реализует DB.Begin с настоящей транзакцией и точечной ошибкой записи.
func (d cloneFailureDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return cloneFailureTx{Tx: tx}, nil
}

type cloneFailureTx struct{ pgx.Tx }

func (tx cloneFailureTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.HasPrefix(sql, "INSERT INTO notebook_cell_data") {
		return pgconn.CommandTag{}, errors.New("injected copy failure")
	}
	return tx.Tx.Exec(ctx, sql, args...)
}

func TestCloneRollsBackPartialDocument(t *testing.T) {
	repo, pool := testRepository(t)
	ctx := t.Context()
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, nil)
	source, err := uc.Create(ctx, 1, "source")
	if err != nil {
		t.Fatal(err)
	}
	broken := NewNotebookRepository(cloneFailureDB{DB: pool})
	if _, err = broken.Clone(ctx, source.ID, 1, "failed copy"); err == nil {
		t.Fatal("injected failure ignored")
	}
	for _, table := range []string{"notebooks", "notebook_cells", "notebook_cell_data"} {
		var count int
		if err = pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s: %d %v", table, count, err)
		}
	}
	if _, err = repo.Get(ctx, source.ID, 1); err != nil {
		t.Fatal("source damaged", err)
	}
}
