package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"github.com/jackc/pgx/v5"
)

const (
	claimSelectSQL = `SELECT id, session_id, kind, attempt FROM runtime_jobs
		WHERE status IN ('queued','running') AND next_run_at <= now() AND (lease_until IS NULL OR lease_until < now())
		ORDER BY next_run_at FOR UPDATE SKIP LOCKED LIMIT 1`
	claimUpdateSQL = `UPDATE runtime_jobs SET status = 'running', lease_owner = $2,
		lease_until = now() + make_interval(secs => $3), attempt = attempt + 1 WHERE id = $1`
	renewSQL = `UPDATE runtime_jobs SET lease_until = now() + make_interval(secs => $3)
		WHERE id = $1 AND lease_owner = $2 AND status = 'running' RETURNING kind`
	completeSQL = `UPDATE runtime_jobs SET status = 'done' WHERE id = $1 AND lease_owner = $2 AND status = 'running' AND kind = $3`
	retrySQL    = `UPDATE runtime_jobs SET status = 'queued', next_run_at = $3, lease_owner = NULL, lease_until = NULL
		WHERE id = $1 AND lease_owner = $2 AND status = 'running'`
)

// Claim берёт готовое задание без действующего lease; (_, false, nil), если заданий нет.
func (r *Repository) Claim(ctx context.Context, owner string, lease time.Duration) (usecase.Job, bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return usecase.Job{}, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var j usecase.Job
	var kind string
	err = tx.QueryRow(ctx, claimSelectSQL).Scan(&j.ID, &j.SessionID, &kind, &j.Attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return usecase.Job{}, false, nil
	}
	if err != nil {
		return usecase.Job{}, false, fmt.Errorf("select job: %w", err)
	}
	if _, err = tx.Exec(ctx, claimUpdateSQL, j.ID, owner, lease.Seconds()); err != nil {
		return usecase.Job{}, false, fmt.Errorf("claim job: %w", err)
	}
	if err = commit(ctx, tx); err != nil {
		return usecase.Job{}, false, err
	}
	j.Kind = usecase.JobKind(kind)
	j.Attempt++
	return j, true, nil
}

// Renew продлевает lease и возвращает текущий вид задания (он мог смениться на delete).
func (r *Repository) Renew(ctx context.Context, job usecase.Job, owner string, lease time.Duration) (usecase.JobKind, error) {
	var kind string
	err := r.db.QueryRow(ctx, renewSQL, job.ID, owner, lease.Seconds()).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", usecase.ErrLeaseLost
	}
	if err != nil {
		return "", fmt.Errorf("renew job: %w", err)
	}
	return usecase.JobKind(kind), nil
}

// Complete завершает задание.
func (r *Repository) Complete(ctx context.Context, job usecase.Job, owner string) error {
	return r.ownedExec(ctx, "complete job", completeSQL, job.ID, owner, string(job.Kind))
}

// Retry возвращает задание в очередь на момент at.
func (r *Repository) Retry(ctx context.Context, job usecase.Job, owner string, at time.Time) error {
	return r.ownedExec(ctx, "retry job", retrySQL, job.ID, owner, at)
}

// ownedExec выполняет изменение задачи при действующем lease.
func (r *Repository) ownedExec(ctx context.Context, what, sql string, args ...any) error {
	tag, err := r.db.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if tag.RowsAffected() == 0 {
		return usecase.ErrLeaseLost
	}
	return nil
}
