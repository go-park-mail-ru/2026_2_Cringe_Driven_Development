package postgres

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSuggestUsersPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "profile_" + uuid.NewString()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}

	repo := NewUserRepository(pool)
	self, err := repo.CreateUser(ctx, "ab_self", "hash")
	if err != nil {
		t.Fatal(err)
	}
	for _, login := range []string{"ab_Z", "AB_a", "abx", "Ab_b", "zebra"} {
		if _, err = repo.CreateUser(ctx, login, "hash"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.SuggestUsers(ctx, self.ID, "AB_", 2)
	if err != nil || len(got) != 2 || got[0].Login != "AB_a" || got[1].Login != "Ab_b" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	got, err = repo.SuggestUsers(ctx, self.ID, "no_match", 10)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	// With a realistic distribution the planner must choose the prefix index.
	if _, err = pool.Exec(ctx, `INSERT INTO users(login,password_hash) SELECT 'user_'||g::text,'hash' FROM generate_series(1,10000) g`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "ANALYZE users"); err != nil {
		t.Fatal(err)
	}
	var plan json.RawMessage
	err = pool.QueryRow(ctx, `EXPLAIN (FORMAT JSON) SELECT id,login FROM users WHERE lower(login) COLLATE "C" LIKE $1 ESCAPE '\' AND id <> $2 ORDER BY lower(login) COLLATE "C",id LIMIT $3`, `ab\_%`, self.ID, 10).Scan(&plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plan), "users_suggest_prefix_idx") {
		t.Fatalf("prefix index unused: %s", plan)
	}
}
