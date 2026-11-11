package postgres

import (
	"context"
	"errors"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/jackc/pgx/v5"
)

// DeleteNotebook атомарно удаляет ноутбук и его завершённую историю runtime.
func (r *NotebookRepository) DeleteNotebook(ctx context.Context, id, ownerID int64) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var notebookID int64
	err = tx.QueryRow(ctx, `SELECT id FROM notebooks WHERE id=$1 AND owner_id=$2 FOR UPDATE`, id, ownerID).Scan(&notebookID)
	if errors.Is(err, pgx.ErrNoRows) {
		return usecase.ErrNotebookNotFound
	}
	if err != nil {
		return err
	}

	// Start, admission и projection тоже блокируют ноутбук раньше строк runtime.
	// Сессии блокируются до проверки флагов, чтобы воркер не изменил их посреди удаления.
	rows, err := tx.Query(ctx, `SELECT state NOT IN ('STOPPED','FAILED')
  OR COALESCE((data->'public'->>'cleanup_pending')::boolean,false)
  OR COALESCE((data->>'CreateUnknown')::boolean,false)
  OR lease_until>now()
  FROM runtime_sessions WHERE notebook_id=$1 ORDER BY id FOR UPDATE`, id)
	if err != nil {
		return err
	}
	busy := false
	for rows.Next() {
		var inUse bool
		if err = rows.Scan(&inUse); err != nil {
			rows.Close()
			return err
		}
		busy = busy || inUse
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if busy {
		return usecase.ErrNotebookInUse
	}
	err = tx.QueryRow(ctx, `SELECT EXISTS (
  SELECT 1 FROM runtime_executions e WHERE e.notebook_id=$1 AND (
   COALESCE(e.data->'Public'->>'state','') NOT IN ('SUCCEEDED','FAILED','INTERRUPTED','TIMED_OUT','CANCELLED','UNKNOWN','REJECTED')
   OR EXISTS (SELECT 1 FROM runtime_projections p WHERE p.session_id=e.session_id AND p.execution_id=e.id)
  ))`, id).Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return usecase.ErrNotebookInUse
	}
	// FK runtime намеренно без каскада; удаляется только полностью завершённая история.
	for _, query := range []string{
		`DELETE FROM runtime_batches WHERE session_id IN (SELECT id FROM runtime_sessions WHERE notebook_id=$1)`,
		`DELETE FROM runtime_events WHERE session_id IN (SELECT id FROM runtime_sessions WHERE notebook_id=$1)`,
		`DELETE FROM runtime_executions WHERE notebook_id=$1`,
		`DELETE FROM runtime_sessions WHERE notebook_id=$1`,
	} {
		if _, err = tx.Exec(ctx, query, id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM notebooks WHERE id=$1 AND owner_id=$2`, id, ownerID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
