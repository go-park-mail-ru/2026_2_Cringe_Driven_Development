package postgres

import (
	"context"
	"encoding/json"
	"time"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Store) checkAdmission(ctx context.Context, tx pgx.Tx, owner int64, sid uuid.UUID) error {
	v, err := readSession(tx.QueryRow(ctx, "SELECT data FROM runtime_sessions WHERE id=$1 AND owner_id=$2", sid, owner))
	if err != nil {
		return err
	}
	if v.StopOperation != "" {
		return D.ErrConflict
	}
	return nil
}

func commandLock(ctx context.Context, tx pgx.Tx, sid uuid.UUID, namespace int) error {
	var ok bool
	if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtextextended($1,$2))", sid.String(), namespace).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return D.ErrConflict
	}
	return nil
}

// DispatchGuard закрывает промежуток между проверкой очереди и отправкой агенту.
func (s *Store) DispatchGuard(ctx context.Context, l D.Lease, call func(context.Context) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = commandLock(ctx, tx, l.Session.ID, 107); err != nil {
		return err
	}
	v, err := s.Owned(ctx, l.Session.OwnerID, l.Session.ID)
	if err != nil {
		return err
	}
	if v.StopOperation != "" {
		return D.ErrConflict
	}
	if err = s.CheckLease(ctx, l); err != nil {
		return err
	}
	return call(ctx)
}

// StopExecutions хранит барьер до подтверждения агента и сохранения результатов.
func (s *Store) StopExecutions(ctx context.Context, owner int64, sid uuid.UUID, resume bool, call func(context.Context, string, int64, []string) ([]D.AgentEvent, error)) error {
	v, err := s.Owned(ctx, owner, sid)
	if err != nil {
		return err
	}
	if v.State != D.Ready {
		return D.ErrConflict
	}
	if !resume && v.StopOperation != "" {
		return D.ErrConflict
	}
	lock, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Rollback(context.Background()) }()
	if err = commandLock(ctx, lock, sid, 106); err != nil {
		return err
	}
	err = s.Mutate(ctx, owner, sid, func(v *D.Session) error {
		if v.State != D.Ready || (!resume && v.StopOperation != "") {
			return D.ErrConflict
		}
		if v.StopOperation == "" {
			v.StopOperation = uuid.NewString()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if _, err = lock.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,107))", sid.String()); err != nil {
		return err
	}
	v, err = s.Owned(ctx, owner, sid)
	if err != nil {
		return err
	}
	snapshot, err := s.db.Query(ctx, `SELECT id FROM runtime_executions WHERE session_id=$1 AND data->'Public'->>'state' NOT IN ('SUCCEEDED','FAILED','INTERRUPTED','TIMED_OUT','CANCELLED','UNKNOWN','REJECTED') ORDER BY admission_order`, sid)
	if err != nil {
		return err
	}
	ids := []string{}
	for snapshot.Next() {
		var id string
		if err = snapshot.Scan(&id); err != nil {
			snapshot.Close()
			return err
		}
		ids = append(ids, id)
	}
	snapshot.Close()
	if err = snapshot.Err(); err != nil {
		return err
	}
	events, callErr := call(ctx, v.StopOperation, v.AgentSeq, ids)
	if callErr != nil && len(events) == 0 {
		return callErr
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var version int64
	err = tx.QueryRow(ctx, "UPDATE runtime_sessions SET lease_version=lease_version+1,lease_until=now()+interval '90 seconds' WHERE id=$1 RETURNING lease_version", sid).Scan(&version)
	if err != nil {
		return err
	}
	local := New(tx)
	l := D.Lease{Session: v, Version: version}
	if err = local.CommitEvents(ctx, l, events); err != nil {
		return err
	}
	v, err = local.Owned(ctx, owner, sid)
	if err != nil {
		return err
	}
	if v.State != D.Ready && callErr == nil {
		callErr = D.ErrUnavailable
	}
	if callErr != nil {
		if v.State != D.Ready {
			v.StopOperation = ""
		}
		raw, e := encodeSession(v)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "UPDATE runtime_sessions SET data=$2,state=$3,lease_version=lease_version+1,lease_until='-infinity',next_check=now() WHERE id=$1", sid, raw, v.State); e != nil {
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		return callErr
	}
	rows, err := tx.Query(ctx, `SELECT data FROM runtime_executions WHERE session_id=$1 AND data->'Public'->>'state' NOT IN ('SUCCEEDED','FAILED','INTERRUPTED','TIMED_OUT','CANCELLED','UNKNOWN','REJECTED') ORDER BY admission_order`, sid)
	if err != nil {
		return err
	}
	var pending []D.Execution
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		x, e := decodeExecution(raw)
		if e != nil {
			rows.Close()
			return e
		}
		pending = append(pending, x)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, x := range pending {
		if x.Dispatch != "PENDING" {
			return D.ErrUnavailable
		}
		x.State = "CANCELLED"
		x.Dispatch = "CANCELLED"
		x.Result = json.RawMessage(`{"reason":"executions_stopped"}`)
		if err = local.SaveExecution(ctx, l, x); err != nil {
			return err
		}
		v.PublicSeq++
		event := map[string]any{"session_id": sid, "execution_id": x.ID, "kind": "execution_state", "state": "CANCELLED", "result": x.Result}
		if x.Kind == "inspect" {
			delete(event, "execution_id")
			event["inspection_id"] = x.ID
		}
		body, e := json.Marshal(event)
		if e != nil {
			return e
		}
		if _, err = tx.Exec(ctx, "INSERT INTO runtime_events(session_id,public_seq,kind,body) VALUES($1,$2,'execution_state',$3)", sid, v.PublicSeq, body); err != nil {
			return err
		}
	}
	v.StopOperation = ""
	v.LastActivityAt = time.Now().UTC()
	v.ActivityPending = true
	raw, err := encodeSession(v)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE runtime_sessions SET data=$2,state=$3,lease_version=lease_version+1,lease_until='-infinity',next_check=now() WHERE id=$1", sid, raw, v.State)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
