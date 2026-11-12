package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v5"
)

func TestRenameNotebookTransactionFailures(t *testing.T) {
	failure := errors.New("injected database error")
	revision := "revision"
	for _, phase := range []string{"begin", "lock", "update", "header", "cells", "commit"} {
		t.Run(phase, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			begin := mock.ExpectBegin()
			if phase == "begin" {
				begin.WillReturnError(failure)
			} else {
				lock := mock.ExpectQuery(regexp.QuoteMeta(selectNotebook+` FOR UPDATE`)).WithArgs(int64(1), int64(1))
				if phase == "lock" {
					lock.WillReturnError(failure)
				} else {
					lock.WillReturnRows(pgxmock.NewRows([]string{"id", "owner_id", "name", "revision", "cells_count", "preview_code", "size_bytes", "created_at", "updated_at"}).AddRow(int64(1), int64(1), "old", &revision, 0, "", int64(40), time.Now(), time.Now()))
					update := mock.ExpectQuery("UPDATE notebooks SET name=").WithArgs(int64(1), int64(1), "new")
					if phase == "update" {
						update.WillReturnError(failure)
					} else {
						update.WillReturnRows(pgxmock.NewRows([]string{"updated_at"}).AddRow(time.Now()))
						header := mock.ExpectQuery("SELECT nbformat,nbformat_minor,metadata,extra FROM notebooks").WithArgs(int64(1))
						if phase == "header" {
							header.WillReturnError(failure)
						} else {
							header.WillReturnRows(pgxmock.NewRows([]string{"nbformat", "nbformat_minor", "metadata", "extra"}).AddRow(4, 5, json.RawMessage(`{}`), map[string]json.RawMessage{}))
							cells := mock.ExpectQuery("SELECT c.cell_id,c.kind,c.source").WithArgs(int64(1))
							if phase == "cells" {
								cells.WillReturnError(failure)
							} else {
								cells.WillReturnRows(pgxmock.NewRows([]string{"cell_id", "kind", "source", "source_version", "metadata", "extra", "outputs", "execution_count", "has_execution_count", "attachments", "output_truncated", "has_runtime_metadata"}))
								mock.ExpectCommit().WillReturnError(failure)
							}
						}
					}
				}
				mock.ExpectRollback()
			}
			got, err := NewNotebookRepository(mock).Rename(t.Context(), 1, 1, "new")
			if !errors.Is(err, failure) || !reflect.DeepEqual(got, domain.Notebook{}) {
				t.Fatalf("phase %s returned %+v %v", phase, got, err)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type renameReadFailureDB struct{ DB }

//nolint:ireturn // Реализует DB.Begin для проверки отката настоящей транзакции.
func (d renameReadFailureDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.DB.Begin(ctx)
	return renameReadFailureTx{tx}, err
}

type renameReadFailureTx struct{ pgx.Tx }

//nolint:ireturn // Реализует pgx.Tx.QueryRow с отказом чтения содержимого.
func (tx renameReadFailureTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.HasPrefix(sql, "SELECT nbformat,") {
		return renameFailedRow{}
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}

type renameFailedRow struct{}

func (renameFailedRow) Scan(...any) error { return errors.New("injected content read failure") }

func TestRenameNotebookRollbackReadFailure(t *testing.T) {
	repo, pool := testRepository(t)
	before := renameNotebook(t, repo, pool)
	got, err := NewNotebookRepository(renameReadFailureDB{pool}).Rename(t.Context(), before.ID, 1, "rollback")
	if err == nil || !reflect.DeepEqual(got, domain.Notebook{}) {
		t.Fatal("read failure returned success", err)
	}
	saved, err := repo.Get(t.Context(), before.ID, 1)
	if err != nil || !reflect.DeepEqual(saved, before) {
		t.Fatal("content failure did not roll back rename", err)
	}
}
