package postgres

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// integrationRepo создаёт отдельную схему PostgreSQL с миграциями (RUNTIME_TEST_DATABASE_URL).
func integrationRepo(t *testing.T) *Repository {
	t.Helper()
	dsn := os.Getenv("RUNTIME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("RUNTIME_TEST_DATABASE_URL is unset")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "runtime_pool_" + uuid.NewString()[:8]
	schema := pgx.Identifier{name}.Sanitize()
	if _, err = root.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); _, _ = root.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); root.Close() })
	if err = migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return NewRepository(pool)
}

// queuedEnv создаёт окружение QUEUED и берёт его задание владельцем owner.
func queuedEnv(t *testing.T, r *Repository, owner string) domain.Environment {
	t.Helper()
	ctx := context.Background()
	env := domain.Environment{SessionID: uuid.New(), OperationID: uuid.New(), Language: "python", Profile: "default",
		State: domain.StateQueued, DesiredState: domain.StateReady, AgentVersion: "v1"}
	env, _, err := r.CreateWithJob(ctx, env, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(ctx, `UPDATE runtime_jobs SET status = 'running', lease_owner = $2 WHERE session_id = $1`,
		env.SessionID, owner); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestPoolRepositoryLifecycle(t *testing.T) {
	r := integrationRepo(t)
	ctx := context.Background()

	added, err := r.ReservePool(ctx, 2, "img", "v1")
	if err != nil || len(added) != 2 {
		t.Fatalf("ReservePool = %d, %v", len(added), err)
	}
	again, err := r.ReservePool(ctx, 2, "img", "v1")
	if err != nil || len(again) != 0 {
		t.Fatalf("second ReservePool = %d, %v", len(again), err)
	}
	vps := added[0]
	vps.Resources = domain.Resources{ServerID: "s", VolumeID: "v", PortID: "p", PrivateIP: "192.168.0.5"}
	vps.HostPrivateKey, vps.HostPublicKey = []byte("sealed"), "ssh-ed25519 AAAA"
	if err = r.SavePreparing(ctx, vps); err != nil {
		t.Fatal(err)
	}
	vps.State = domain.PoolReady
	if err = r.SavePreparing(ctx, vps); err != nil {
		t.Fatal(err)
	}
	if err = r.SavePreparing(ctx, vps); !errors.Is(err, usecase.ErrStale) {
		t.Fatalf("save of a READY vps = %v, want ErrStale", err)
	}

	// Две сессии одновременно: READY-VPS одна, она достаётся ровно одной.
	a, b := queuedEnv(t, r, "w1"), queuedEnv(t, r, "w1")
	var wg sync.WaitGroup
	got := make([]bool, 2)
	for i, env := range []domain.Environment{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, cerr := r.ClaimPool(ctx, env, "w1", "img", "v1")
			if cerr != nil {
				t.Error(cerr)
			}
			got[i] = ok
		}()
	}
	wg.Wait()
	if got[0] == got[1] {
		t.Fatalf("claims = %v, want exactly one", got)
	}
	winner := a
	if got[1] {
		winner = b
	}
	env, err := r.Get(ctx, winner.SessionID)
	if err != nil || env.State != domain.StateVerifying || env.PoolID != vps.ID || env.Resources != vps.Resources {
		t.Fatalf("claimed env = %+v, %v", env, err)
	}
	sec, err := r.LoadSecrets(ctx, winner.SessionID)
	if err != nil || string(sec.HostPrivateKey) != "sealed" || sec.HostPublicKey != "ssh-ed25519 AAAA" {
		t.Fatalf("secrets = %+v, %v", sec, err)
	}

	// Save сохраняет pool_id и снимает его.
	env.State = domain.StateCreating
	env.PoolID, env.Resources = uuid.Nil, domain.Resources{}
	if env, err = r.Save(ctx, env, "w1"); err != nil {
		t.Fatal(err)
	}
	if env, err = r.Get(ctx, winner.SessionID); err != nil || env.PoolID != uuid.Nil {
		t.Fatalf("env after leaving pool = %+v, %v", env, err)
	}

	rows, err := r.ListPool(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("ListPool = %+v, %v", rows, err)
	}
	for _, row := range rows {
		claimed := row.ID == vps.ID && row.State == domain.PoolClaimed && row.SessionID == winner.SessionID &&
			row.Resources == vps.Resources
		preparing := row.ID == added[1].ID && row.State == domain.PoolPreparing
		if !claimed && !preparing {
			t.Fatalf("pool row = %+v", row)
		}
	}
	if err = r.MarkPoolDeleting(ctx, vps.ID, "assign failed"); err != nil {
		t.Fatal(err)
	}
	if err = r.DeletePool(ctx, vps.ID); err != nil {
		t.Fatal(err)
	}
	if rows, err = r.ListPool(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("ListPool after delete = %+v, %v", rows, err)
	}
	// Другие образ или версия не считаются: пул текущей конфигурации пополняется.
	if fresh, err := r.ReservePool(ctx, 2, "img-2", "v1"); err != nil || len(fresh) != 2 {
		t.Fatalf("ReservePool for a new image = %d, %v", len(fresh), err)
	}
}
