package postgres

import (
	"testing"

	"github.com/pashagolub/pgxmock/v5"
)

const (
	createUserSQL    = `INSERT INTO users (login, password_hash) VALUES ($1, $2) RETURNING id`
	byLoginSQL       = `SELECT id, login, password_hash, COALESCE(avatar_key, '') FROM users WHERE lower(login) = lower($1)`
	byIDSQL          = `SELECT login, COALESCE(avatar_key, '') FROM users WHERE id = $1`
	createSessionSQL = `INSERT INTO refresh_sessions (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`
	deleteSessionSQL = `DELETE FROM refresh_sessions WHERE token_hash = $1 RETURNING user_id, expires_at`
)

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
