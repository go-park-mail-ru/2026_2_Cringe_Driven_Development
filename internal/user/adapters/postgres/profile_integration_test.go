package postgres

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TEST_POSTGRES_DSN указывает на одноразовую БД. Каждый прогон владеет своей схемой.
func TestProfilePostgresIntegration(t *testing.T) {
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
	a, err := repo.CreateUser(ctx, "Alice", "oldhash")
	if err != nil {
		t.Fatal(err)
	}
	b, err := repo.CreateUser(ctx, "Bob", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.UpdateLogin(ctx, b.ID, "aLiCe"); !errors.Is(err, usecase.ErrLoginTaken) {
		t.Fatalf("login conflict: %v", err)
	}
	// У двух изменений с одним и тем же наблюдённым хешем ровно один победитель.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, hash := range []string{"new1", "new2"} {
		wg.Add(1)
		go func() { defer wg.Done(); results <- repo.CompareAndSwapPassword(ctx, a.ID, "oldhash", hash) }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err = range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, usecase.ErrInvalidCurrentPassword) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("CAS winners=%d", successes)
	}
	// Конкурентные замены должны вернуть цепочку: одна видит пустое значение, другая видит предшественника.
	type swap struct {
		key, old string
		err      error
	}
	swaps := make(chan swap, 2)
	for _, key := range []string{"key1", "key2"} {
		wg.Add(1)
		go func() { defer wg.Done(); _, old, e := repo.SwapAvatar(ctx, a.ID, key); swaps <- swap{key, old, e} }()
	}
	wg.Wait()
	close(swaps)
	predecessors := map[string]string{}
	for s := range swaps {
		if s.err != nil {
			t.Fatal(s.err)
		}
		predecessors[s.key] = s.old
	}
	final, err := repo.UserByID(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	old := predecessors[final.AvatarKey]
	if old == "" || predecessors[old] != "" {
		t.Fatalf("non-serial swaps: final=%s history=%v", final.AvatarKey, predecessors)
	}
	var updated, created time.Time
	if err = pool.QueryRow(ctx, `SELECT updated_at, created_at FROM users WHERE id=$1`, a.ID).Scan(&updated, &created); err != nil || !updated.After(created) {
		t.Fatalf("updated=%s created=%s err=%v", updated, created, err)
	}
}
