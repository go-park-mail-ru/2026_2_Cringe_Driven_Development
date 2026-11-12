package postgres

import (
	"context"
	"time"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
)

// Control сериализует управление kernel с приёмом команд; restart разрешается только при полностью свободной
// очереди.
func (s *Store) Control(ctx context.Context, owner int64, sid uuid.UUID, idle bool, call func(context.Context) (string, error)) (string, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	v, err := readSession(tx.QueryRow(ctx, "SELECT data FROM runtime_sessions WHERE id=$1 AND owner_id=$2 FOR UPDATE", sid, owner))
	if err != nil {
		return "", err
	}
	if v.State != D.Ready || v.StopOperation != "" {
		return "", D.ErrConflict
	}
	if idle {
		var count int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM runtime_executions WHERE session_id=$1 AND data->'Public'->>'state' NOT IN ('SUCCEEDED','FAILED','INTERRUPTED','TIMED_OUT','CANCELLED','UNKNOWN','REJECTED')`, sid).Scan(&count)
		if err != nil {
			return "", err
		}
		if count != 0 {
			return "", D.ErrConflict
		}
	}
	generation, err := call(ctx)
	if err != nil {
		return "", err
	}
	if generation != "" {
		v.Generation = generation
	}
	v.LastActivityAt = time.Now().UTC()
	v.ActivityPending = true
	raw, err := encodeSession(v)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, "UPDATE runtime_sessions SET data=$2,next_check=now(),lease_version=lease_version+1,lease_until='-infinity' WHERE id=$1", sid, raw)
	if err != nil {
		return "", err
	}
	return generation, tx.Commit(ctx)
}
