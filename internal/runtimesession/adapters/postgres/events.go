package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/usecase"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CommitEvents одной транзакцией сохраняет события, обновляет результаты и курсор; уже записанные seq
// повторно не применяются.
func (s *Store) CommitEvents(ctx context.Context, l domain.Lease, batch []domain.AgentEvent) error {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var raw []byte
	var valid bool
	e = tx.QueryRow(ctx, "SELECT data,lease_version=$2 AND lease_until>now() FROM runtime_sessions WHERE id=$1 FOR UPDATE", l.Session.ID, l.Version).Scan(&raw, &valid)
	if e != nil {
		return e
	}
	if !valid {
		return domain.ErrLeaseLost
	}
	v, e := decodeSession(raw)
	if e != nil {
		return e
	}
	rows, e := tx.Query(ctx, "SELECT data FROM runtime_executions WHERE session_id=$1 FOR UPDATE", v.ID)
	if e != nil {
		return e
	}
	var xs []domain.Execution
	for rows.Next() {
		x, err := readExecution(rows)
		if err != nil {
			rows.Close()
			return err
		}
		xs = append(xs, x)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	changed := map[int]bool{}
	for _, ev := range batch {
		if ev.SessionID != v.ID || ev.Seq <= 0 {
			return domain.ErrProtocol
		}
		if ev.Seq <= v.AgentSeq {
			continue
		}
		publicEvent := any(ev)
		if ev.ExecutionID != "" {
			for _, x := range xs {
				if x.ID == ev.ExecutionID && x.Kind == "inspect" {
					publicEvent = struct {
						domain.AgentEvent
						ExecutionID  string `json:"execution_id,omitempty"`
						InspectionID string `json:"inspection_id"`
					}{AgentEvent: ev, InspectionID: ev.ExecutionID}
					break
				}
			}
		}
		body, err := json.Marshal(publicEvent)
		if err != nil {
			return err
		}
		v.PublicSeq++
		_, e = tx.Exec(ctx, "INSERT INTO runtime_events(session_id,public_seq,agent_seq,kind,body) VALUES($1,$2,$3,$4,$5)", v.ID, v.PublicSeq, ev.Seq, ev.Kind, body)
		if e != nil {
			return e
		}
		v.AgentSeq = ev.Seq
		if ev.Kind == "session_state" && (ev.State == "STOPPED" || ev.State == "FAILED") {
			v.State = domain.Stopping
			v.Stage = "stopping"
			v.Reason = ev.Reason
		}
		if ev.Generation != "" {
			v.Generation = ev.Generation
		}
		for i := range xs {
			if xs[i].Kind == "inspect" && xs[i].ID != ev.ExecutionID {
				continue
			}
			if xs[i].ID != ev.ExecutionID && (ev.Kind != "kernel_message" || ev.MsgType != "update_display_data" || xs[i].Generation != ev.Generation) {
				continue
			}
			if e = xs[i].Apply(ev); e != nil {
				return e
			}
			changed[i] = true
		}
	}
	for i := range changed {
		x := xs[i]
		b, err := encodeExecution(x)
		if err != nil {
			return err
		}
		_, e = tx.Exec(ctx, "UPDATE runtime_executions SET data=$3 WHERE session_id=$1 AND id=$2", v.ID, x.ID, b)
		if e != nil {
			return e
		}
		if x.Projection == "pending" {
			_, e = tx.Exec(ctx, "INSERT INTO runtime_projections(session_id,execution_id,version) VALUES($1,$2,$3) ON CONFLICT(session_id,execution_id) DO UPDATE SET version=EXCLUDED.version,next_attempt=now()", v.ID, x.ID, x.Version)
			if e != nil {
				return e
			}
		}
	}
	b, e := encodeSession(v)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "UPDATE runtime_sessions SET data=$2,state=$3 WHERE id=$1", v.ID, b, v.State)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// Events возвращает события после указанного курсора; чтение не продлевает активность пользователя.
func (s *Store) Events(ctx context.Context, owner int64, id uuid.UUID, after int64, limits domain.EventLimits) (domain.EventPage, error) {
	page := domain.EventPage{Events: []domain.Event{}, NextCursor: after}
	if _, err := s.Owned(ctx, owner, id); err != nil {
		return page, err
	}
	if limits.Count <= 0 || limits.Count > 100 {
		limits.Count = 100
	}
	if limits.Bytes <= 0 {
		limits.Bytes = 16 << 20
	}
	rows, err := s.db.Query(ctx, "SELECT public_seq,kind,body FROM runtime_events WHERE session_id=$1 AND public_seq>$2 ORDER BY public_seq LIMIT $3", id, after, limits.Count+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	size := 0
	for rows.Next() {
		var ev domain.Event
		if err = rows.Scan(&ev.Seq, &ev.Kind, &ev.Body); err != nil {
			return page, err
		}
		if len(page.Events) >= limits.Count || (len(page.Events) > 0 && size+len(ev.Body) > limits.Bytes) {
			page.HasMore = true
			break
		}
		size += len(ev.Body)
		page.Events = append(page.Events, ev)
		page.NextCursor = ev.Seq
	}
	return page, rows.Err()
}

// Project применяет отложенный результат к актуальной ячейке под блокировкой блокнота, защищая более новую
// попытку и изменённый код.
func (s *Store) Project(ctx context.Context, w usecase.OutputWriter) error {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sid uuid.UUID
	var id string
	var version int64
	e = tx.QueryRow(ctx, "SELECT session_id,execution_id,version FROM runtime_projections WHERE next_attempt<=now() ORDER BY next_attempt,session_id,execution_id LIMIT 1").Scan(&sid, &id, &version)
	if errors.Is(e, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if e != nil {
		return e
	}
	var key string
	e = tx.QueryRow(ctx, "SELECT n.file_key FROM notebooks n JOIN runtime_executions e ON e.notebook_id=n.id WHERE e.session_id=$1 AND e.id=$2 FOR UPDATE OF n", sid, id).Scan(&key)
	if e != nil {
		return e
	}
	x, e := readExecution(tx.QueryRow(ctx, "SELECT data FROM runtime_executions WHERE session_id=$1 AND id=$2 FOR UPDATE", sid, id))
	if e != nil {
		return e
	}
	if x.Projection != "pending" {
		return domain.ErrNotFound
	}
	var latest int64
	e = tx.QueryRow(ctx, "SELECT MAX(attempt) FROM runtime_executions WHERE notebook_id=$1 AND cell_id=$2", x.NotebookID, x.CellID).Scan(&latest)
	if e != nil {
		return e
	}
	applied := false
	var newKey string
	if latest == x.Attempt {
		newKey, applied, e = w.ApplyOutput(ctx, key, x)
		if e != nil {
			_ = tx.Rollback(ctx)
			_, retryErr := s.db.Exec(ctx, "UPDATE runtime_projections SET next_attempt=now()+interval '5 seconds' WHERE session_id=$1 AND execution_id=$2 AND version=$3", sid, id, version)
			return errors.Join(e, retryErr)
		}
	}
	x.Projection = "conflict"
	if applied {
		x.Projection = "applied"
		_, e = tx.Exec(ctx, "UPDATE notebooks SET file_key=$2 WHERE id=$1", x.NotebookID, newKey)
		if e != nil {
			return e
		}
	}
	b, e := encodeExecution(x)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "UPDATE runtime_executions SET data=$3 WHERE session_id=$1 AND id=$2", sid, id, b)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "DELETE FROM runtime_projections WHERE session_id=$1 AND execution_id=$2 AND version<=$3", sid, id, x.Version)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
