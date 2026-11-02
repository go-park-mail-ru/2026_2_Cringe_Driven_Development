package postgres

import (
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v5"
)

const (
	createSQL = `INSERT INTO notebooks (owner_id, name, file_key, cells_count) VALUES ($1, $2, $3, $4) RETURNING id, created_at, updated_at`
	listSQL   = `SELECT id, name, cells_count, updated_at FROM notebooks WHERE owner_id = $1 ORDER BY updated_at DESC`
	getSQL    = `SELECT id, owner_id, name, file_key, cells_count, created_at, updated_at FROM notebooks WHERE id = $1 AND owner_id = $2`
	lockSQL   = `SELECT id, owner_id, name, file_key, cells_count, created_at, updated_at FROM notebooks WHERE id = $1 AND owner_id = $2 FOR UPDATE`
	updateSQL = `UPDATE notebooks SET file_key = $2, cells_count = $3, updated_at = now() WHERE id = $1`
)

func fixtureTime() time.Time { return time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC) }

func notebookRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "owner_id", "name", "file_key", "cells_count", "created_at", "updated_at"}).AddRow(int64(17), int64(23), "SECRET-name", "SECRET-key", 1, fixtureTime(), fixtureTime())
}
func newPool(t *testing.T) pgxmock.PgxPoolIface { //nolint:ireturn // pgxmock exposes an interface
	t.Helper()
	db, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		db.Close()
	})
	return db
}
