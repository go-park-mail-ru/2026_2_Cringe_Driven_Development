package postgres

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func twoFactorPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "totp_" + uuid.NewString()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, dropErr := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE")
		if dropErr != nil {
			t.Error(dropErr)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestTwoFactorDatabaseRejectsImpossibleStates(t *testing.T) {
	pool := twoFactorPool(t)
	ctx := context.Background()
	u, err := NewUserRepository(pool).CreateUser(ctx, "alice", "hash")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		enabled bool
		secret  []byte
		step    int64
	}{
		{true, nil, 0}, {true, []byte{}, 0}, {true, []byte("cipher"), -1}, {false, nil, 0}, {false, []byte("cipher"), 0},
	} {
		_, err = pool.Exec(ctx, `UPDATE users SET totp_enabled=$2, totp_secret=$3, totp_last_step=$4 WHERE id=$1`, u.ID, tc.enabled, tc.secret, tc.step)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("impossible state enabled=%v secret_present=%v step=%d accepted: %v", tc.enabled, tc.secret != nil, tc.step, err)
		}
	}
}
