// Package postgres сохраняет намерения runtime, владение задачами и ключи идемпотентности.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	notebookpostgres "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/postgres"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB определяет используемые операции PostgreSQL и позволяет подставлять управляемую базу в тестах.
type DB interface {
	// Begin начинает транзакцию PostgreSQL.
	Begin(context.Context) (pgx.Tx, error)
	// Query читает набор строк PostgreSQL.
	Query(context.Context, string, ...any) (pgx.Rows, error)
	// QueryRow читает одну строку PostgreSQL.
	QueryRow(context.Context, string, ...any) pgx.Row
	// Exec выполняет SQL-команду без чтения строк.
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// Store хранит сессии, команды, события и владение фоновыми задачами в PostgreSQL.
type Store struct {
	// db Подключение к PostgreSQL.
	db DB
	// notebooks использует ту же транзакцию, что и публикация результатов ячеек.
	notebooks *notebookpostgres.NotebookRepository
}

// New подключает PostgreSQL и чтение исходников к хранилищу runtime-сессий.
func New(db DB) *Store { return &Store{db: db, notebooks: notebookpostgres.NewNotebookRepository(db)} }

// sessionRecord сохраняет публичную сессию вместе с внутренними флагами восстановления и очистки.
type sessionRecord struct {
	StopOperation string
	// DeleteSent Признак отправленного запроса удаления окружения.
	DeleteSent bool
	// AgentStarted Признак ранее подтверждённого запуска агента.
	AgentStarted bool
	// StopRetry Число попыток остановки агента.
	StopRetry int
	// JournalRetry Число неудачных попыток чтения журнала.
	JournalRetry int
	// Public Публичная часть записи, разрешённая для ответа API.
	Public domain.Session `json:"public"`
	// OwnerID ID владельца для проверки доступа.
	OwnerID int64
	// RequestID Ключ идемпотентности пользовательского запроса.
	RequestID string
	// ActivityPending Необходимость передать агенту ещё не доставленное действие пользователя.
	ActivityPending, CreateSent, CreateUnknown, JournalArchived bool
	// AgentSeq Последний номер события агента, подтверждённый транзакцией архива.
	AgentSeq int64
	// Retry Счётчик повторных попыток текущего этапа.
	Retry int
	// NextCheck Время следующего шага фоновой обработки.
	NextCheck time.Time
}

// encodeSession сохраняет сессию вместе со служебными полями, исключёнными из публичного API.
func encodeSession(s domain.Session) ([]byte, error) {
	return json.Marshal(sessionRecord{StopOperation: s.StopOperation, Public: s, DeleteSent: s.DeleteSent, AgentStarted: s.AgentStarted, OwnerID: s.OwnerID, RequestID: s.RequestID, ActivityPending: s.ActivityPending, CreateSent: s.CreateSent, CreateUnknown: s.CreateUnknown, JournalArchived: s.JournalArchived, AgentSeq: s.AgentSeq, Retry: s.Retry, NextCheck: s.NextCheck, StopRetry: s.StopRetry, JournalRetry: s.JournalRetry})
}

// decodeSession восстанавливает публичные и служебные поля сессии из записи PostgreSQL.
func decodeSession(b []byte) (domain.Session, error) {
	var r sessionRecord
	e := json.Unmarshal(b, &r)
	s := r.Public
	s.StopOperation = r.StopOperation
	s.DeleteSent = r.DeleteSent
	s.AgentStarted = r.AgentStarted
	s.StopRetry = r.StopRetry
	s.JournalRetry = r.JournalRetry
	s.OwnerID = r.OwnerID
	s.RequestID = r.RequestID
	s.ActivityPending = r.ActivityPending
	s.CreateSent = r.CreateSent
	s.CreateUnknown = r.CreateUnknown
	s.JournalArchived = r.JournalArchived
	s.AgentSeq = r.AgentSeq
	s.Retry = r.Retry
	s.NextCheck = r.NextCheck
	return s, e
}

// readSession читает одну сессию и возвращает отдельную ошибку при отсутствии записи.
func readSession(row pgx.Row) (domain.Session, error) {
	var b []byte
	e := row.Scan(&b)
	if errors.Is(e, pgx.ErrNoRows) {
		return domain.Session{}, domain.ErrNotFound
	}
	if e != nil {
		return domain.Session{}, e
	}
	return decodeSession(b)
}

// Start атомарно сохраняет запуск сессии, проверяя владельца и единственный активный слот пользователя.
func (s *Store) Start(ctx context.Context, v domain.Session) (domain.Session, error) {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return v, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ownerID int64
	if e = tx.QueryRow(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", v.OwnerID).Scan(&ownerID); errors.Is(e, pgx.ErrNoRows) {
		return v, domain.ErrNotFound
	} else if e != nil {
		return v, e
	}
	var notebookID int64
	e = tx.QueryRow(ctx, "SELECT id FROM notebooks WHERE id=$1 AND owner_id=$2 FOR UPDATE", v.NotebookID, v.OwnerID).Scan(&notebookID)
	if errors.Is(e, pgx.ErrNoRows) {
		return v, domain.ErrNotFound
	}
	if e != nil {
		return v, e
	}
	prior, e := readSession(tx.QueryRow(ctx, "SELECT data FROM runtime_sessions WHERE owner_id=$1 AND request_id=$2", v.OwnerID, v.RequestID))
	if e == nil {
		if prior.NotebookID != v.NotebookID || prior.Language != v.Language {
			return v, domain.ErrConflict
		}
		return prior, tx.Commit(ctx)
	}
	if !errors.Is(e, domain.ErrNotFound) {
		return v, e
	}
	active, err := readSession(tx.QueryRow(ctx, "SELECT data FROM runtime_sessions WHERE owner_id=$1 AND state NOT IN ('STOPPED','FAILED')", v.OwnerID))
	if err == nil {
		return v, &domain.ActiveSessionError{Session: active}
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return v, err
	}
	b, e := encodeSession(v)
	if e != nil {
		return v, e
	}
	_, e = tx.Exec(ctx, "INSERT INTO runtime_sessions(id,owner_id,notebook_id,request_id,language,state,data) VALUES($1,$2,$3,$4,$5,$6,$7)", v.ID, v.OwnerID, v.NotebookID, v.RequestID, v.Language, v.State, b)
	if e != nil {
		return v, translate(e)
	}
	return v, tx.Commit(ctx)
}

// translate сопоставляет нарушение уникальности в PostgreSQL конфликту runtime, сохраняя остальные ошибки.
func translate(e error) error {
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23505" {
		return domain.ErrConflict
	}
	return e
}

// Owned возвращает сессию только указанного владельца; чужой ID не раскрывается.
func (s *Store) Owned(ctx context.Context, owner int64, id uuid.UUID) (domain.Session, error) {
	return readSession(s.db.QueryRow(ctx, "SELECT data FROM runtime_sessions WHERE id=$1 AND owner_id=$2", id, owner))
}

// Claim выбирает готовую к обработке сессию и временно закрепляет её за одним фоновым обработчиком.
func (s *Store) Claim(ctx context.Context, worker string) (domain.Lease, error) {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return domain.Lease{}, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var b []byte
	var version int64
	e = tx.QueryRow(ctx, `UPDATE runtime_sessions SET lease_owner=$1,lease_version=lease_version+1,lease_until=now()+interval '90 seconds'
 WHERE id=(SELECT id FROM runtime_sessions WHERE (state NOT IN ('STOPPED','FAILED') OR (data->>'CreateUnknown')::boolean OR COALESCE((data->'public'->>'cleanup_pending')::boolean,false)) AND next_check<=now() AND lease_until<now() ORDER BY next_check FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING data,lease_version`, worker).Scan(&b, &version)
	if errors.Is(e, pgx.ErrNoRows) {
		return domain.Lease{}, domain.ErrNotFound
	}
	if e != nil {
		return domain.Lease{}, e
	}
	v, e := decodeSession(b)
	if e != nil {
		return domain.Lease{}, e
	}
	v.LeaseVersion = version
	return domain.Lease{Session: v, Version: version}, tx.Commit(ctx)
}

// CheckLease проверяет, что фоновый обработчик ещё владеет задачей и может выполнять внешние действия.
func (s *Store) CheckLease(ctx context.Context, l domain.Lease) error {
	var ok bool
	e := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM runtime_sessions WHERE id=$1 AND lease_version=$2 AND lease_until>now())", l.Session.ID, l.Version).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return domain.ErrLeaseLost
	}
	return nil
}

// Save сохраняет шаг обработки сессии и освобождает её для следующего прохода.
func (s *Store) Save(ctx context.Context, l domain.Lease, v domain.Session) error {
	return s.save(ctx, l, v, true)
}

// Checkpoint сохраняет промежуточное состояние сессии, не освобождая текущее владение задачей.
func (s *Store) Checkpoint(ctx context.Context, l domain.Lease, v domain.Session) error {
	return s.save(ctx, l, v, false)
}

// save сохраняет сессию под проверкой версии владения, не затирая уже записанные курсоры и события.
func (s *Store) save(ctx context.Context, l domain.Lease, v domain.Session, release bool) error {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var raw []byte
	var ok bool
	e = tx.QueryRow(ctx, "SELECT data,lease_version=$2 AND lease_until>now() FROM runtime_sessions WHERE id=$1 FOR UPDATE", v.ID, l.Version).Scan(&raw, &ok)
	if e != nil {
		return e
	}
	if !ok {
		return domain.ErrLeaseLost
	}
	current, e := decodeSession(raw)
	if e != nil {
		return e
	}
	v.StopOperation = current.StopOperation
	v.AgentSeq = current.AgentSeq
	v.PublicSeq = current.PublicSeq
	if current.AgentSeq > l.Session.AgentSeq {
		v.Generation = current.Generation
	}
	if current.State != v.State || current.Stage != v.Stage || current.Reason != v.Reason || current.CleanupError != v.CleanupError {
		v.PublicSeq++
		var body []byte
		body, e = json.Marshal(map[string]any{"state": v.State, "stage": v.Stage, "reason": v.Reason, "cleanup_pending": v.CleanupError})
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, "INSERT INTO runtime_events(session_id,public_seq,kind,body) VALUES($1,$2,'session_state',$3)", v.ID, v.PublicSeq, body)
		if e != nil {
			return e
		}
	}
	b, e := encodeSession(v)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "UPDATE runtime_sessions SET state=$2,data=$3,next_check=$4,lease_until=CASE WHEN $5 THEN '-infinity'::timestamptz ELSE lease_until END WHERE id=$1", v.ID, v.State, b, v.NextCheck, release)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// Mutate атомарно меняет принадлежащую пользователю сессию и отменяет устаревшее владение фоновой задачей.
func (s *Store) Mutate(ctx context.Context, owner int64, id uuid.UUID, fn func(*domain.Session) error) error {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	v, e := readSession(tx.QueryRow(ctx, "SELECT data FROM runtime_sessions WHERE id=$1 AND owner_id=$2 FOR UPDATE", id, owner))
	if e != nil {
		return e
	}
	if e = fn(&v); e != nil {
		return e
	}
	b, e := encodeSession(v)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "UPDATE runtime_sessions SET state=$2,data=$3,next_check=now(),lease_version=lease_version+1,lease_until='-infinity' WHERE id=$1", id, v.State, b)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// Active возвращает единственную незавершённую сессию пользователя либо явное отсутствие сессии.
func (s *Store) Active(ctx context.Context, owner int64) (*domain.Session, error) {
	v, err := readSession(s.db.QueryRow(ctx, "SELECT data FROM runtime_sessions WHERE owner_id=$1 AND state NOT IN ('STOPPED','FAILED')", owner))
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// ActiveNotebook отдаёт сводки ноутбуков из того же авторитетного состояния сессий.
func (s *Store) ActiveNotebook(ctx context.Context, owner int64) (int64, error) {
	session, err := s.Active(ctx, owner)
	if err != nil {
		return 0, err
	}
	if session == nil {
		return 0, nil
	}
	return session.NotebookID, nil
}
