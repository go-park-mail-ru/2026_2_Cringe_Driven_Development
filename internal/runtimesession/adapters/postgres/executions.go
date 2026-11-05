package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// executionRecord сохраняет публичный результат и служебные параметры команды отдельно, чтобы последние не
// попали в API.
type executionRecord struct {
	// Public Публичная часть записи, разрешённая для ответа API.
	Public domain.Execution
	// OwnerID ID владельца для проверки доступа.
	OwnerID, NotebookID int64
	// Code Снимок исходника, который должен выполнить агент.
	Code string
	// TimeoutSeconds Допустимое время выполнения команды в секундах.
	TimeoutSeconds float64
	// Dispatch Служебное состояние доставки команды агенту.
	Dispatch string
	// Version Версия состояния для защиты согласованного обновления.
	Version int64
	// Generation UUID поколения kernel, меняющийся при перезапуске.
	Generation string
	// ClearPending Отложенная очистка вывода до следующего сообщения при clear_output с wait=true.
	ClearPending bool
	// Kind Вид команды, события или ячейки, определяющий обработку.
	Kind string
}

// encodeExecution сериализует публичный результат и внутренние параметры исполнения для хранения в
// PostgreSQL.
func encodeExecution(x domain.Execution) ([]byte, error) {
	return json.Marshal(executionRecord{x, x.OwnerID, x.NotebookID, x.Code, x.TimeoutSeconds, x.Dispatch, x.Version, x.Generation, x.ClearPending, x.Kind})
}

// decodeExecution восстанавливает исполнение вместе с внутренними полями, которые не выдаются клиенту.
func decodeExecution(b []byte) (domain.Execution, error) {
	var r executionRecord
	e := json.Unmarshal(b, &r)
	x := r.Public
	x.OwnerID = r.OwnerID
	x.NotebookID = r.NotebookID
	x.Code = r.Code
	x.TimeoutSeconds = r.TimeoutSeconds
	x.Dispatch = r.Dispatch
	x.Version = r.Version
	x.Generation = r.Generation
	x.ClearPending = r.ClearPending
	x.Kind = r.Kind
	return x, e
}

// readExecution читает одну запись исполнения и отличает отсутствие записи от ошибки базы.
func readExecution(row pgx.Row) (domain.Execution, error) {
	var b []byte
	e := row.Scan(&b)
	if errors.Is(e, pgx.ErrNoRows) {
		return domain.Execution{}, domain.ErrNotFound
	}
	if e != nil {
		return domain.Execution{}, e
	}
	return decodeExecution(b)
}

// Execution читает сохранённое исполнение, проверяя принадлежность сессии владельцу.
func (s *Store) Execution(ctx context.Context, owner int64, sid uuid.UUID, id string) (domain.Execution, error) {
	return readExecution(s.db.QueryRow(ctx, "SELECT e.data FROM runtime_executions e JOIN runtime_sessions s ON s.id=e.session_id WHERE e.session_id=$1 AND e.id=$2 AND s.owner_id=$3", sid, id, owner))
}

// Admit под блокировкой проверяет владельца, состояние сессии и source_hash, затем сохраняет команду до
// отправки агенту.
func (s *Store) Admit(ctx context.Context, owner int64, sid uuid.UUID, r domain.ExecuteRequest) (domain.Execution, error) {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return domain.Execution{}, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var key string
	var nb int64
	e = tx.QueryRow(ctx, "SELECT n.file_key,n.id FROM notebooks n JOIN runtime_sessions s ON s.notebook_id=n.id WHERE s.id=$1 AND s.owner_id=$2 AND n.owner_id=$2 FOR UPDATE OF n", sid, owner).Scan(&key, &nb)
	if errors.Is(e, pgx.ErrNoRows) {
		return domain.Execution{}, domain.ErrNotFound
	}
	if e != nil {
		return domain.Execution{}, e
	}
	prior, e := readExecution(tx.QueryRow(ctx, "SELECT data FROM runtime_executions WHERE session_id=$1 AND id=$2", sid, r.ExecutionID))
	if e == nil {
		if !prior.Same(r) {
			return prior, domain.ErrConflict
		}
		return prior, tx.Commit(ctx)
	}
	if !errors.Is(e, domain.ErrNotFound) {
		return prior, e
	}
	v, e := readSession(tx.QueryRow(ctx, "SELECT data FROM runtime_sessions WHERE id=$1 FOR UPDATE", sid))
	if e != nil {
		return prior, e
	}
	if v.State != domain.Ready {
		return prior, domain.ErrConflict
	}
	var queued int
	e = tx.QueryRow(ctx, `SELECT count(*) FROM runtime_executions WHERE session_id=$1 AND data->'Public'->>'state' NOT IN ('SUCCEEDED','FAILED','INTERRUPTED','TIMED_OUT','CANCELLED','UNKNOWN','REJECTED')`, sid).Scan(&queued)
	if e != nil {
		return prior, e
	}
	if queued >= domain.MaxQueuedCommands {
		return prior, domain.ErrQueueFull
	}
	code, e := s.reader.ReadCode(ctx, key, r.CellID)
	if e != nil {
		return prior, e
	}
	if len(code) > 65536 {
		return prior, domain.ErrInvalid
	}
	hash := sha256.Sum256([]byte(code))
	if hex.EncodeToString(hash[:]) != r.SourceHash {
		return prior, domain.ErrConflict
	}
	if _, e = domain.CommandPayload(domain.Command{ExecutionID: r.ExecutionID, Code: code, TimeoutSeconds: r.TimeoutSeconds}); e != nil {
		return prior, e
	}
	var attempt int64
	e = tx.QueryRow(ctx, "SELECT COALESCE(MAX(attempt),0)+1 FROM runtime_executions WHERE notebook_id=$1 AND cell_id=$2", nb, r.CellID).Scan(&attempt)
	if e != nil {
		return prior, e
	}
	x := domain.Execution{ID: r.ExecutionID, SessionID: sid, OwnerID: owner, NotebookID: nb, CellID: r.CellID, SourceHash: r.SourceHash, Code: code, TimeoutSeconds: r.TimeoutSeconds, Attempt: attempt, Dispatch: "PENDING", State: "PENDING", Outputs: []json.RawMessage{}, Projection: "pending"}
	b, e := encodeExecution(x)
	if e != nil {
		return x, e
	}
	_, e = tx.Exec(ctx, "INSERT INTO runtime_executions(session_id,id,notebook_id,cell_id,attempt,data) VALUES($1,$2,$3,$4,$5,$6)", sid, x.ID, nb, x.CellID, attempt, b)
	if e != nil {
		return x, translate(e)
	}
	v.LastActivityAt = time.Now().UTC()
	v.ActivityPending = true
	sessionData, err := encodeSession(v)
	if err != nil {
		return x, err
	}
	_, e = tx.Exec(ctx, "UPDATE runtime_sessions SET data=$2,next_check=now(),lease_version=lease_version+1,lease_until='-infinity' WHERE id=$1", sid, sessionData)
	if e != nil {
		return x, e
	}
	return x, tx.Commit(ctx)
}

// Executions читает команды сессии в устойчивом порядке принятия из базы, а не по ID или номеру ячейки.
func (s *Store) Executions(ctx context.Context, sid uuid.UUID) ([]domain.Execution, error) {
	rows, e := s.db.Query(ctx, "SELECT data FROM runtime_executions WHERE session_id=$1 ORDER BY admission_order", sid)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var xs []domain.Execution
	for rows.Next() {
		x, e := readExecution(rows)
		if e != nil {
			return nil, e
		}
		xs = append(xs, x)
	}
	return xs, rows.Err()
}

// SaveExecution сохраняет состояние команды только при действующем владении фоновой задачей и ставит
// завершённый результат на запись в ячейку.
func (s *Store) SaveExecution(ctx context.Context, l domain.Lease, x domain.Execution) error {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ok bool
	e = tx.QueryRow(ctx, "SELECT lease_version=$2 AND lease_until>now() FROM runtime_sessions WHERE id=$1 FOR UPDATE", l.Session.ID, l.Version).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return domain.ErrLeaseLost
	}
	b, e := encodeExecution(x)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "UPDATE runtime_executions SET data=$3 WHERE session_id=$1 AND id=$2", x.SessionID, x.ID, b)
	if e != nil {
		return e
	}
	if x.Projection == "pending" && x.Terminal() && x.State != "REJECTED" {
		_, e = tx.Exec(ctx, "INSERT INTO runtime_projections(session_id,execution_id,version) VALUES($1,$2,$3) ON CONFLICT(session_id,execution_id) DO UPDATE SET version=EXCLUDED.version,next_attempt=now()", x.SessionID, x.ID, x.Version)
		if e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
