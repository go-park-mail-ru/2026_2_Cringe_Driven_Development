package postgres

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/observability"
	"github.com/pashagolub/pgxmock/v5"
)

func TestGetLogsScanFailureWithoutErrorText(t *testing.T) {
	var out bytes.Buffer
	var level slog.LevelVar
	level.Set(slog.LevelDebug)
	old := slog.Default()
	slog.SetDefault(slog.New(observability.NewHandler(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: &level}))))
	t.Cleanup(func() { slog.SetDefault(old) })
	db := newPool(t)
	db.ExpectQuery(getSQL).WithArgs(int64(1), int64(2)).WillReturnError(errors.New("SECRET-row"))
	repo := NewNotebookRepository(db)
	_, err := repo.Get(observability.WithRequestID(context.Background(), "req-db"), 1, 2)
	if err == nil {
		t.Fatal("expected scan error")
	}
	log := out.String()
	if !strings.Contains(log, `"operation":"notebook.get"`) || !strings.Contains(log, `"outcome":"error"`) || !strings.Contains(log, `"request_id":"req-db"`) || strings.Contains(log, "SECRET") {
		t.Fatalf("unsafe log: %s", log)
	}
}

func TestNotebookRepositoryLogsFailedOperationsOnce(t *testing.T) {
	var out bytes.Buffer
	var level slog.LevelVar
	level.Set(slog.LevelDebug)
	old := slog.Default()
	slog.SetDefault(slog.New(observability.NewHandler(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: &level}))))
	t.Cleanup(func() { slog.SetDefault(old) })
	ctx := observability.WithRequestID(context.Background(), "req-db")
	db := newPool(t)
	failure := errors.New("SECRET-db")
	db.ExpectQuery(createSQL).WithArgs(int64(0), "SECRET-name", "", 0).WillReturnError(failure)
	db.ExpectQuery(listSQL).WithArgs(int64(2)).WillReturnError(failure)
	db.ExpectQuery(getSQL).WithArgs(int64(1), int64(2)).WillReturnError(failure)
	db.ExpectBegin().WillReturnError(failure)
	repo := NewNotebookRepository(db)
	for _, tc := range []struct {
		operation string
		call      func() error
	}{
		{"notebook.create", func() error { _, err := repo.Create(ctx, domain.Notebook{Name: "SECRET-name"}); return err }},
		{"notebook.list", func() error { _, err := repo.List(ctx, 2); return err }},
		{"notebook.get", func() error { _, err := repo.Get(ctx, 1, 2); return err }},
		{"notebook.modify", func() error {
			return repo.Modify(ctx, 1, 2, func(domain.Notebook) (usecase.MetadataUpdate, error) {
				return usecase.MetadataUpdate{FileKey: "next", CellsCount: 2, Changed: true}, nil
			})
		}},
	} {
		out.Reset()
		if err := tc.call(); !errors.Is(err, failure) {
			t.Fatalf("%s unexpectedly succeeded", tc.operation)
		}
		log := out.String()
		if strings.Count(log, `"operation":"`+tc.operation+`"`) != 1 || !strings.Contains(log, `"outcome":"error"`) || !strings.Contains(log, `"request_id":"req-db"`) || strings.Contains(log, "SECRET") {
			t.Fatalf("%s log: %s", tc.operation, log)
		}
	}
}

func TestNotebookListLogsCollectRowsError(t *testing.T) {
	var out bytes.Buffer
	var level slog.LevelVar
	level.Set(slog.LevelDebug)
	old := slog.Default()
	slog.SetDefault(slog.New(observability.NewHandler(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: &level}))))
	t.Cleanup(func() { slog.SetDefault(old) })
	source := errors.New("SECRET-rows")
	db := newPool(t)
	db.ExpectQuery(listSQL).WithArgs(int64(2)).WillReturnRows(pgxmock.NewRows([]string{"id", "name", "cells_count", "updated_at"}).AddRow(int64(1), "SECRET", 1, fixtureTime()).RowError(0, source)).RowsWillBeClosed()
	repo := NewNotebookRepository(db)
	_, err := repo.List(context.Background(), 2)
	if !errors.Is(err, source) || strings.Count(out.String(), `"operation":"notebook.list"`) != 1 || !strings.Contains(out.String(), `"outcome":"error"`) || strings.Contains(out.String(), "SECRET") {
		t.Fatalf("err=%v log=%s", err, out.String())
	}
}

func TestNotebookModifyLogsCommitError(t *testing.T) {
	var out bytes.Buffer
	var level slog.LevelVar
	level.Set(slog.LevelDebug)
	old := slog.Default()
	slog.SetDefault(slog.New(observability.NewHandler(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: &level}))))
	t.Cleanup(func() { slog.SetDefault(old) })
	source := errors.New("SECRET-commit")
	db := newPool(t)
	db.ExpectBegin()
	db.ExpectQuery(lockSQL).WithArgs(int64(1), int64(2)).WillReturnRows(notebookRows())
	db.ExpectExec(updateSQL).WithArgs(int64(1), "next", 2).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	db.ExpectCommit().WillReturnError(source)
	db.ExpectRollback()
	repo := NewNotebookRepository(db)
	err := repo.Modify(context.Background(), 1, 2, func(domain.Notebook) (usecase.MetadataUpdate, error) {
		return usecase.MetadataUpdate{FileKey: "next", CellsCount: 2, Changed: true}, nil
	})
	if !errors.Is(err, source) || strings.Count(out.String(), `"operation":"notebook.modify"`) != 1 || !strings.Contains(out.String(), `"outcome":"error"`) || strings.Contains(out.String(), "SECRET") {
		t.Fatalf("err=%v log=%s", err, out.String())
	}
}

func TestModifyNoOpDoesNotUpdateTimestamp(t *testing.T) {
	db := newPool(t)
	db.ExpectBegin()
	db.ExpectQuery(lockSQL).WithArgs(int64(17), int64(23)).WillReturnRows(notebookRows())
	db.ExpectCommit()
	db.ExpectRollback()
	err := NewNotebookRepository(db).Modify(context.Background(), 17, 23, func(domain.Notebook) (usecase.MetadataUpdate, error) {
		return usecase.MetadataUpdate{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
