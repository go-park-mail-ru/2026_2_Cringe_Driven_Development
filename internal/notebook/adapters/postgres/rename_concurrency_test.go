package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// lockDB ставит барьер после реального захвата строки или сообщает PID ожидающего.
type lockDB struct {
	DB
	acquired chan struct{}
	release  <-chan struct{}
	waiter   chan uint32
}

//nolint:ireturn // Реализует DB.Begin с типом pgx.Tx.
func (d lockDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.DB.Begin(ctx)
	return lockTx{Tx: tx, gate: d}, err
}

type lockTx struct {
	pgx.Tx
	gate lockDB
}

//nolint:ireturn // Реализует pgx.Tx.QueryRow для наблюдения блокировки.
func (tx lockTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if !strings.Contains(sql, "FROM notebooks") || !strings.Contains(sql, "FOR UPDATE") {
		return tx.Tx.QueryRow(ctx, sql, args...)
	}
	if tx.gate.waiter != nil {
		tx.gate.waiter <- tx.Conn().PgConn().PID()
	}
	return lockRow{Row: tx.Tx.QueryRow(ctx, sql, args...), ctx: ctx, gate: tx.gate}
}

type lockRow struct {
	pgx.Row
	ctx  context.Context
	gate lockDB
}

func (r lockRow) Scan(dest ...any) error {
	if err := r.Row.Scan(dest...); err != nil {
		return err
	}
	if r.gate.acquired != nil {
		close(r.gate.acquired)
		select {
		case <-r.gate.release:
		case <-r.ctx.Done():
			return r.ctx.Err()
		}
	}
	return nil
}

//nolint:ireturn // Возвращает конкретное значение из типизированного канала теста.
func awaitRename[T any](ctx context.Context, t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-ctx.Done():
		t.Fatal("rename synchronization timed out", ctx.Err())
		var zero T
		return zero
	}
}

func awaitNotebookLock(ctx context.Context, t *testing.T, pool *pgxpool.Pool, pid uint32) {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, int32(pid)).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("second operation did not wait for notebook lock", ctx.Err())
		}
	}
}

func serializeRename(t *testing.T, pool *pgxpool.Pool, first, second func(*NotebookRepository) error) (error, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	gate := lockDB{DB: pool, acquired: make(chan struct{}), release: release}
	done1, done2 := make(chan error, 1), make(chan error, 1)
	go func() { done1 <- first(NewNotebookRepository(gate)) }()
	awaitRename(ctx, t, gate.acquired)
	waiter := lockDB{DB: pool, waiter: make(chan uint32, 1)}
	go func() { done2 <- second(NewNotebookRepository(waiter)) }()
	pid := awaitRename(ctx, t, waiter.waiter)
	awaitNotebookLock(ctx, t, pool, pid)
	close(release)
	return awaitRename(ctx, t, done1), awaitRename(ctx, t, done2)
}

func TestRenameNotebookAndEditingSerialize(t *testing.T) {
	testRenameWithWrite(t, false)
}

func TestRenameNotebookAndResultSerialize(t *testing.T) {
	testRenameWithWrite(t, true)
}

func testRenameWithWrite(t *testing.T, result bool) {
	t.Helper()
	for _, renameFirst := range []bool{true, false} {
		name := "write first"
		if renameFirst {
			name = "rename first"
		}
		t.Run(name, func(t *testing.T) {
			repo, pool := testRepository(t)
			before := renameNotebook(t, repo, pool)
			ctx := t.Context()
			var renamed domain.Notebook
			rename := func(r *NotebookRepository) error {
				var err error
				renamed, err = r.Rename(ctx, before.ID, 1, "renamed")
				return err
			}
			write := func(r *NotebookRepository) error {
				if result {
					_, err := r.SaveResult(ctx, before.ID, 1, usecase.CellResult{CellID: "code", SourceVersion: before.Cells[0].SourceVersion, Outputs: []byte(`[{"text":"after rename"}]`), OutputTruncated: false})
					return err
				}
				_, err := r.Apply(ctx, before.ID, 1, before.Revision, usecase.Operation{Kind: usecase.Edit, CellID: "code", Source: "print(43)"})
				return err
			}
			first, second := rename, write
			if !renameFirst {
				first, second = write, rename
			}
			if err1, err2 := serializeRename(t, pool, first, second); err1 != nil || err2 != nil {
				t.Fatalf("serialized writes: %v %v", err1, err2)
			}
			got, err := repo.Get(ctx, before.ID, 1)
			if err != nil || got.Name != "renamed" || renamed.Name != "renamed" {
				t.Fatalf("rename lost: %+v %v", got, err)
			}
			if result {
				if string(got.Cells[0].Outputs) != `[{"text":"after rename"}]` || got.Cells[0].OutputTruncated || got.Cells[0].ExecutionCount != nil || got.Revision != before.Revision || got.Cells[0].SourceVersion != before.Cells[0].SourceVersion {
					t.Fatalf("result/revision lost: %+v", got)
				}
			} else if got.Cells[0].Source != "print(43)" || got.Revision == before.Revision || got.Cells[0].SourceVersion == before.Cells[0].SourceVersion {
				t.Fatalf("edit lost: %+v", got)
			}
			wantSource := before.Cells[0].Source
			if !result && !renameFirst {
				wantSource = "print(43)"
			}
			if renamed.Cells[0].Source != wantSource {
				t.Fatal("rename response is not its transaction snapshot")
			}
		})
	}
}

func TestRenameNotebookConcurrentRenames(t *testing.T) {
	for _, secondName := range []string{"first", "second"} {
		t.Run(secondName, func(t *testing.T) {
			repo, pool := testRepository(t)
			n := renameNotebook(t, repo, pool)
			var first, second domain.Notebook
			one := func(r *NotebookRepository) error {
				var e error
				first, e = r.Rename(t.Context(), n.ID, 1, "first")
				return e
			}
			two := func(r *NotebookRepository) error {
				var e error
				second, e = r.Rename(t.Context(), n.ID, 1, secondName)
				return e
			}
			if e1, e2 := serializeRename(t, pool, one, two); e1 != nil || e2 != nil {
				t.Fatalf("renames: %v %v", e1, e2)
			}
			got, err := repo.Get(t.Context(), n.ID, 1)
			if err != nil || first.Name != "first" || second.Name != secondName || got.Name != secondName {
				t.Fatalf("wrong order: %+v %v", got, err)
			}
			if secondName == "first" && !first.UpdatedAt.Equal(second.UpdatedAt) {
				t.Fatal("concurrent no-op changed timestamp")
			}
		})
	}
}

func TestRenameNotebookAndDeleteSerialize(t *testing.T) {
	for _, renameFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "rename first", false: "delete first"}[renameFirst], func(t *testing.T) {
			repo, pool := testRepository(t)
			n := renameNotebook(t, repo, pool)
			rename := func(r *NotebookRepository) error { _, err := r.Rename(t.Context(), n.ID, 1, "renamed"); return err }
			del := func(r *NotebookRepository) error { return r.DeleteNotebook(t.Context(), n.ID, 1) }
			first, second := rename, del
			if !renameFirst {
				first, second = del, rename
			}
			e1, e2 := serializeRename(t, pool, first, second)
			if e1 != nil || (renameFirst && e2 != nil) || (!renameFirst && !errors.Is(e2, usecase.ErrNotebookNotFound)) {
				t.Fatalf("delete order: %v %v", e1, e2)
			}
			if _, err := repo.Get(t.Context(), n.ID, 1); !errors.Is(err, usecase.ErrNotebookNotFound) {
				t.Fatal("rename recreated notebook", err)
			}
		})
	}
}

func TestRenameNotebookCanceledWhileLocked(t *testing.T) {
	repo, pool := testRepository(t)
	n := renameNotebook(t, repo, pool)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var id int64
	if err = tx.QueryRow(ctx, `SELECT id FROM notebooks WHERE id=$1 FOR UPDATE`, n.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	request, stop := context.WithCancel(ctx)
	defer stop()
	waiter := lockDB{DB: pool, waiter: make(chan uint32, 1)}
	done := make(chan error, 1)
	go func() {
		_, renameErr := NewNotebookRepository(waiter).Rename(request, n.ID, 1, "canceled")
		done <- renameErr
	}()
	awaitNotebookLock(ctx, t, pool, awaitRename(ctx, t, waiter.waiter))
	stop()
	if err = awaitRename(ctx, t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled rename: %v", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, n.ID, 1)
	if err != nil || got.Name != "original" || !got.UpdatedAt.Equal(n.UpdatedAt) {
		t.Fatal("cancellation changed notebook", err)
	}
	if _, err = repo.Rename(ctx, n.ID, 1, "after cancellation"); err != nil {
		t.Fatal("transaction leaked", err)
	}
}
