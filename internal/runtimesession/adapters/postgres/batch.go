package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Queue читает только незавершённые исполнения ячеек: выполняемое первым, затем ожидающие; инспекции не
// включаются.
func (s *Store) Queue(ctx context.Context, owner int64, sid uuid.UUID) (D.Queue, error) {
	q := D.Queue{SessionID: sid, Items: []D.QueueItem{}}
	if _, err := s.Owned(ctx, owner, sid); err != nil {
		return q, err
	}
	rows, err := s.db.Query(ctx, `SELECT id,cell_id,CASE WHEN data->'Public'->>'state'='RUNNING' THEN 'RUNNING' ELSE 'QUEUED' END
 FROM runtime_executions WHERE session_id=$1 AND cell_id!=''
 AND data->'Public'->>'state' NOT IN ('SUCCEEDED','FAILED','INTERRUPTED','TIMED_OUT','CANCELLED','UNKNOWN','REJECTED')
 ORDER BY (data->'Public'->>'state'='RUNNING') DESC,admission_order`, sid)
	if err != nil {
		return q, err
	}
	defer rows.Close()
	for rows.Next() {
		var item D.QueueItem
		if err = rows.Scan(&item.ExecutionID, &item.CellID, &item.State); err != nil {
			return q, err
		}
		q.Items = append(q.Items, item)
	}
	if err = rows.Err(); err != nil {
		return q, err
	}
	return q, nil
}

// AdmitBatch атомарно проверяет и сохраняет весь фиксированный пакет ячеек; повтор request_id возвращает
// прежние IDs.
func (s *Store) AdmitBatch(ctx context.Context, owner int64, sid uuid.UUID, r D.BatchRequest) (D.Batch, error) {
	out := D.Batch{SessionID: sid, RequestID: r.RequestID, ExecutionIDs: []string{}}
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
	payload, err := json.Marshal(r)
	if err != nil {
		return out, err
	}
	var priorRequest, priorResult []byte
	err = tx.QueryRow(ctx, "SELECT request,result FROM runtime_batches WHERE session_id=$1 AND request_id=$2", sid, r.RequestID).Scan(&priorRequest, &priorResult)
	if err == nil {
		var prior D.BatchRequest
		if err = json.Unmarshal(priorRequest, &prior); err != nil {
			return out, err
		}
		priorJSON, _ := json.Marshal(prior)
		if string(priorJSON) != string(payload) {
			return out, D.ErrConflict
		}
		if err = json.Unmarshal(priorResult, &out); err != nil {
			return out, err
		}
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
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
	if count+len(r.Cells) > D.MaxQueuedCommands {
		return out, D.ErrQueueFull
	}
	previous := -1
	for _, c := range r.Cells {
		code, sourceVersion, position, readErr := readCell(ctx, tx, nb, c.CellID)
		if readErr != nil {
			return out, readErr
		}
		if position <= previous {
			return out, D.ErrConflict
		}
		previous = position
		if len(code) > 65536 {
			return out, D.ErrInvalid
		}
		hash := sha256.Sum256([]byte(code))
		if hex.EncodeToString(hash[:]) != c.SourceHash {
			return out, D.ErrConflict
		}
		id := uuid.NewString()
		var attempt int64
		err = tx.QueryRow(ctx, "SELECT COALESCE(MAX(attempt),0)+1 FROM runtime_executions WHERE notebook_id=$1 AND cell_id=$2", nb, c.CellID).Scan(&attempt)
		if err != nil {
			return out, err
		}
		x := D.Execution{SessionID: sid, ID: id, OwnerID: owner, NotebookID: nb, CellID: c.CellID, SourceHash: c.SourceHash, SourceVersion: sourceVersion, Code: code, TimeoutSeconds: r.TimeoutSeconds, Attempt: attempt, Dispatch: "PENDING", State: "PENDING", Outputs: []json.RawMessage{}, Projection: "pending"}
		if _, err = D.CommandPayload(D.Command{ExecutionID: id, Code: code, TimeoutSeconds: r.TimeoutSeconds}); err != nil {
			return out, err
		}
		data, encodeErr := encodeExecution(x)
		if encodeErr != nil {
			return out, encodeErr
		}
		if _, err = tx.Exec(ctx, "INSERT INTO runtime_executions(session_id,id,notebook_id,cell_id,attempt,data) VALUES($1,$2,$3,$4,$5,$6)", sid, id, nb, c.CellID, attempt, data); err != nil {
			return out, translate(err)
		}
		out.ExecutionIDs = append(out.ExecutionIDs, id)
	}
	result, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO runtime_batches(session_id,request_id,request,result) VALUES($1,$2,$3,$4)", sid, r.RequestID, payload, result); err != nil {
		return out, translate(err)
	}
	v.LastActivityAt = time.Now().UTC()
	v.ActivityPending = true
	raw, err := encodeSession(v)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, "UPDATE runtime_sessions SET data=$2,next_check=now(),lease_version=lease_version+1,lease_until='-infinity' WHERE id=$1", sid, raw)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
