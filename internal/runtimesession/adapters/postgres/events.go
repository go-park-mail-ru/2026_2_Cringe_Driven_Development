package postgres

import (
	"context"
	"encoding/json"
	"errors"

	notebookusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
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
	for _, ev := range batch {
		if ev.SessionID != v.ID || ev.Seq <= 0 {
			return domain.ErrProtocol
		}
		if ev.Seq <= v.AgentSeq {
			continue
		}
		var command *domain.Execution
		if ev.ExecutionID != "" {
			x, err := readExecution(tx.QueryRow(ctx, "SELECT data FROM runtime_executions WHERE session_id=$1 AND id=$2 FOR UPDATE", v.ID, ev.ExecutionID))
			if err != nil && !errors.Is(err, domain.ErrNotFound) {
				return err
			}
			if err == nil {
				command = &x
			}
		}
		publicEvent := any(ev)
		if command != nil && command.Kind == "inspect" {
			publicEvent = struct {
				domain.AgentEvent
				ExecutionID  string `json:"execution_id,omitempty"`
				InspectionID string `json:"inspection_id"`
			}{AgentEvent: ev, InspectionID: ev.ExecutionID}
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
		if command != nil {
			if e = applyExecution(ctx, tx, *command, ev); e != nil {
				return e
			}
		}
		if ev.Kind == "kernel_message" && ev.MsgType == "update_display_data" {
			if e = updateDisplays(ctx, tx, ev); e != nil {
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
func (s *Store) Project(ctx context.Context) error {
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
	var notebookID int64
	e = tx.QueryRow(ctx, "SELECT n.id FROM notebooks n JOIN runtime_executions e ON e.notebook_id=n.id WHERE e.session_id=$1 AND e.id=$2 FOR UPDATE OF n", sid, id).Scan(&notebookID)
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
	if latest == x.Attempt {
		applied, e = s.applyOutput(ctx, tx, x)
		// Неподдерживаемые значения JSONB и переполнение размера ноутбука считаются постоянными
		// конфликты проекции; архив выполнения без потерь остаётся доступен.
		if errors.Is(e, notebookusecase.ErrInvalidDocument) || errors.Is(e, notebookusecase.ErrTooLarge) {
			applied = false
			e = nil
		}
		if e != nil {
			_ = tx.Rollback(ctx)
			_, retryErr := s.db.Exec(ctx, "UPDATE runtime_projections SET next_attempt=now()+interval '5 seconds' WHERE session_id=$1 AND execution_id=$2 AND version=$3", sid, id, version)
			return errors.Join(e, retryErr)
		}
	}
	x.Projection = "conflict"
	if applied {
		x.Projection = "applied"
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

func (s *Store) applyOutput(ctx context.Context, tx pgx.Tx, x domain.Execution) (bool, error) {
	outputs := make([]map[string]json.RawMessage, 0, len(x.Outputs))
	for _, raw := range x.Outputs {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return false, err
		}
		if fields == nil {
			return false, domain.ErrProtocol
		}
		delete(fields, "transient")
		outputs = append(outputs, fields)
	}
	encoded, err := json.Marshal(outputs)
	if err != nil {
		return false, err
	}
	_, err = s.notebooks.SaveResultTx(ctx, tx, x.NotebookID, x.OwnerID, notebookusecase.CellResult{CellID: x.CellID, SourceVersion: x.SourceVersion, Outputs: encoded, ExecutionCount: x.ExecutionCount, OutputTruncated: x.Truncated})
	if errors.Is(err, notebookusecase.ErrStaleSource) {
		return false, nil
	}
	return err == nil, err
}
