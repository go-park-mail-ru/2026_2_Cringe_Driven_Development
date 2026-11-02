package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v5"
)

func TestNotebookRepositoryParameterizedQueries(t *testing.T) {
	db := newPool(t)
	r := NewNotebookRepository(db)
	ctx := context.Background()
	payload := "x'); DROP TABLE notebooks; --"
	db.ExpectQuery(createSQL).WithArgs(int64(23), payload, "notebooks/file.ipynb", 1).WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(17), fixtureTime(), fixtureTime()))
	n, err := r.Create(ctx, domain.Notebook{OwnerID: 23, Name: payload, FileKey: "notebooks/file.ipynb", CellsCount: 1})
	if err != nil || n.ID != 17 || n.Name != payload || !n.CreatedAt.Equal(fixtureTime()) || !n.UpdatedAt.Equal(fixtureTime()) {
		t.Fatalf("notebook=%v err=%v", n, err)
	}
	db.ExpectQuery(listSQL).WithArgs(int64(23)).WillReturnRows(pgxmock.NewRows([]string{"id", "name", "cells_count", "updated_at"}).AddRow(int64(17), payload, 1, fixtureTime())).RowsWillBeClosed()
	list, err := r.List(ctx, 23)
	if err != nil || len(list) != 1 || list[0].Name != payload || list[0].ID != 17 || list[0].CellsCount != 1 || !list[0].UpdatedAt.Equal(fixtureTime()) {
		t.Fatalf("list=%v err=%v", list, err)
	}
	db.ExpectQuery(getSQL).WithArgs(int64(17), int64(23)).WillReturnRows(notebookRows())
	n, err = r.Get(ctx, 17, 23)
	if err != nil || n.ID != 17 || n.OwnerID != 23 || n.Name != "SECRET-name" || n.FileKey != "SECRET-key" || n.CellsCount != 1 || !n.CreatedAt.Equal(fixtureTime()) || !n.UpdatedAt.Equal(fixtureTime()) {
		t.Fatalf("notebook=%v err=%v", n, err)
	}
}

func TestNotebookListRows(t *testing.T) {
	for _, name := range []string{"empty", "scan", "late error"} {
		t.Run(name, func(t *testing.T) {
			db := newPool(t)
			r := NewNotebookRepository(db)
			rows := pgxmock.NewRows([]string{"id", "name", "cells_count", "updated_at"})
			source := errors.New("iteration failed")
			switch name {
			case "scan":
				rows.AddRow("not-an-id", "name", 1, fixtureTime())
			case "late error":
				rows.AddRow(int64(17), "name", 1, fixtureTime()).AddRow(int64(18), "other", 2, fixtureTime()).RowError(1, source)
			}
			db.ExpectQuery(listSQL).WithArgs(int64(23)).WillReturnRows(rows).RowsWillBeClosed()
			got, err := r.List(context.Background(), 23)
			switch name {
			case "empty":
				if err != nil || got == nil || len(got) != 0 {
					t.Fatalf("list=%v err=%v", got, err)
				}
			case "scan":
				if err == nil {
					t.Fatal("expected scan failure")
				}
			case "late error":
				if !errors.Is(err, source) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestNotebookGetNotFound(t *testing.T) {
	db := newPool(t)
	db.ExpectQuery(getSQL).WithArgs(int64(17), int64(99)).WillReturnError(pgx.ErrNoRows)
	_, err := NewNotebookRepository(db).Get(context.Background(), 17, 99)
	if !errors.Is(err, usecase.ErrNotebookNotFound) {
		t.Fatal(err)
	}
}

func TestNotebookModifyTransactions(t *testing.T) {
	for _, stage := range []string{"success", "begin", "missing", "select", "callback", "update", "commit", "rollback"} {
		t.Run(stage, func(t *testing.T) {
			db := newPool(t)
			r := NewNotebookRepository(db)
			source := errors.New(stage + " failed")
			var want error
			called := false
			begin := db.ExpectBegin()
			if stage == "begin" {
				begin.WillReturnError(source)
				want = source
			} else {
				q := db.ExpectQuery(lockSQL).WithArgs(int64(17), int64(23))
				switch stage {
				case "missing":
					q.WillReturnError(pgx.ErrNoRows)
					want = usecase.ErrNotebookNotFound
				case "select":
					q.WillReturnError(source)
					want = source
				default:
					q.WillReturnRows(notebookRows())
				}
				if stage != "missing" && stage != "select" {
					if stage == "callback" {
						want = source
					} else {
						upd := db.ExpectExec(updateSQL).WithArgs(int64(17), "next", 3)
						if stage == "update" {
							upd.WillReturnError(source)
							want = source
						} else {
							upd.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
							commit := db.ExpectCommit()
							if stage == "commit" {
								commit.WillReturnError(source)
								want = source
							}
						}
					}
				}
				// The repository always defers Rollback, including after Commit.
				rollback := db.ExpectRollback()
				if stage == "rollback" {
					rollback.WillReturnError(source)
				}
			}
			err := r.Modify(context.Background(), 17, 23, func(n domain.Notebook) (usecase.MetadataUpdate, error) {
				called = true
				if n.ID != 17 || n.OwnerID != 23 || n.FileKey != "SECRET-key" || n.CellsCount != 1 {
					t.Fatalf("callback notebook=%+v", n)
				}
				if stage == "callback" {
					return usecase.MetadataUpdate{}, source
				}
				return usecase.MetadataUpdate{FileKey: "next", CellsCount: 3, Changed: true}, nil
			})
			if !errors.Is(err, want) {
				t.Fatalf("err=%v want=%v", err, want)
			}
			shouldCall := stage != "begin" && stage != "missing" && stage != "select"
			if called != shouldCall {
				t.Fatalf("callback=%v want=%v", called, shouldCall)
			}
		})
	}
}
