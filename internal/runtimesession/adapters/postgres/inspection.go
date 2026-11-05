package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AdmitInspection сохраняет идемпотентную инспекцию в общей очереди команд, не создавая для неё фиктивную
// ячейку.
func (s *Store) AdmitInspection(ctx context.Context, owner int64, sid uuid.UUID, id string) (D.Execution, error) {
	var out D.Execution
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var nb int64
	err = tx.QueryRow(ctx, "SELECT n.id FROM notebooks n JOIN runtime_sessions s ON s.notebook_id=n.id WHERE s.id=$1 AND s.owner_id=$2 AND n.owner_id=$2 FOR UPDATE OF n", sid, owner).Scan(&nb)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, D.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	prior, err := readExecution(tx.QueryRow(ctx, "SELECT data FROM runtime_executions WHERE session_id=$1 AND id=$2", sid, id))
	if err == nil {
		if prior.Kind != "inspect" {
			return out, D.ErrConflict
		}
		return prior, tx.Commit(ctx)
	}
	if !errors.Is(err, D.ErrNotFound) {
		return out, err
	}
	v, err := readSession(tx.QueryRow(ctx, "SELECT data FROM runtime_sessions WHERE id=$1 FOR UPDATE", sid))
	if err != nil {
		return out, err
	}
	if v.State != D.Ready {
		return out, D.ErrConflict
	}
	var count int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM runtime_executions WHERE session_id=$1 AND data->'Public'->>'state' NOT IN ('SUCCEEDED','FAILED','INTERRUPTED','TIMED_OUT','CANCELLED','UNKNOWN','REJECTED')`, sid).Scan(&count)
	if err != nil {
		return out, err
	}
	if count >= D.MaxQueuedCommands {
		return out, D.ErrQueueFull
	}
	var attempt int64
	err = tx.QueryRow(ctx, "SELECT COALESCE(MAX(attempt),0)+1 FROM runtime_executions WHERE notebook_id=$1 AND cell_id=''", nb).Scan(&attempt)
	if err != nil {
		return out, err
	}
	out = D.Execution{Kind: "inspect", ID: id, SessionID: sid, OwnerID: owner, NotebookID: nb, TimeoutSeconds: 5, Attempt: attempt, Dispatch: "PENDING", State: "PENDING", Outputs: []json.RawMessage{}, Projection: "none"}
	raw, err := encodeExecution(out)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO runtime_executions(session_id,id,notebook_id,cell_id,attempt,data) VALUES($1,$2,$3,'',$4,$5)", sid, id, nb, attempt, raw)
	if err != nil {
		return out, translate(err)
	}
	v.ActivityPending = true
	v.LastActivityAt = time.Now().UTC()
	raw, err = encodeSession(v)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, "UPDATE runtime_sessions SET data=$2,next_check=now(),lease_version=lease_version+1,lease_until='-infinity' WHERE id=$1", sid, raw)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
