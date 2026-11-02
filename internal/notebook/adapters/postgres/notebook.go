// Package postgres хранит метаданные блокнотов в PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/observability"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB описывает нужное от *pgxpool.Pool: запросы и транзакции.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

const selectNotebook = `SELECT id, owner_id, name, file_key, cells_count, created_at, updated_at
	FROM notebooks WHERE id = $1 AND owner_id = $2`

// NotebookRepository реализует usecase.Metadata на таблице notebooks.
type NotebookRepository struct {
	db DB
}

// NewNotebookRepository принимает пул соединений.
func NewNotebookRepository(db DB) *NotebookRepository {
	return &NotebookRepository{db: db}
}

// Create вставляет строку; id и время создания выдаёт база.
func (r *NotebookRepository) Create(ctx context.Context, n domain.Notebook) (domain.Notebook, error) {
	started := time.Now()
	err := r.db.QueryRow(ctx,
		`INSERT INTO notebooks (owner_id, name, file_key, cells_count) VALUES ($1, $2, $3, $4)
		RETURNING id, created_at, updated_at`,
		n.OwnerID, n.Name, n.FileKey, n.CellsCount,
	).Scan(&n.ID, &n.CreatedAt, &n.UpdatedAt)
	observability.LogStorage(ctx, "notebook.create", started, err)
	if err != nil {
		return domain.Notebook{}, fmt.Errorf("insert notebook: %w", err)
	}
	return n, nil
}

// List отдаёт блокноты владельца, сначала недавно изменённые.
func (r *NotebookRepository) List(ctx context.Context, ownerID int64) ([]domain.NotebookSummary, error) {
	started := time.Now()
	rows, err := r.db.Query(ctx,
		`SELECT id, name, cells_count, updated_at FROM notebooks
		WHERE owner_id = $1 ORDER BY updated_at DESC`,
		ownerID,
	)
	if err != nil {
		observability.LogStorage(ctx, "notebook.list", started, err)
		return nil, fmt.Errorf("select notebooks: %w", err)
	}
	list, err := pgx.CollectRows(rows, scanSummary)
	observability.LogStorage(ctx, "notebook.list", started, err)
	if err != nil {
		return nil, fmt.Errorf("scan notebooks: %w", err)
	}
	return list, nil
}

// Get ищет блокнот по id только среди блокнотов владельца.
func (r *NotebookRepository) Get(ctx context.Context, id, ownerID int64) (domain.Notebook, error) {
	started := time.Now()
	n, err := scanNotebook(r.db.QueryRow(ctx, selectNotebook, id, ownerID))
	observability.LogStorage(ctx, "notebook.get", started, err)
	return n, err
}

// Modify выполняет fn под SELECT ... FOR UPDATE и атомарно публикует новую редакцию.
func (r *NotebookRepository) Modify(ctx context.Context, id, ownerID int64, fn func(domain.Notebook) (usecase.MetadataUpdate, error)) (err error) {
	started := time.Now()
	defer func() { observability.LogStorage(ctx, "notebook.modify", started, err) }()
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	n, err := scanNotebook(tx.QueryRow(ctx, selectNotebook+` FOR UPDATE`, id, ownerID))
	if err != nil {
		return err
	}
	update, err := fn(n)
	if err != nil {
		return err
	}
	if update.Changed {
		_, err = tx.Exec(ctx,
			`UPDATE notebooks SET file_key = $2, cells_count = $3, updated_at = now() WHERE id = $1`,
			id, update.FileKey, update.CellsCount,
		)
		if err != nil {
			return fmt.Errorf("update notebook: %w", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func scanNotebook(row pgx.Row) (domain.Notebook, error) {
	var n domain.Notebook
	err := row.Scan(&n.ID, &n.OwnerID, &n.Name, &n.FileKey, &n.CellsCount, &n.CreatedAt, &n.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Notebook{}, usecase.ErrNotebookNotFound
	}
	if err != nil {
		return domain.Notebook{}, fmt.Errorf("select notebook: %w", err)
	}
	return n, nil
}

func scanSummary(row pgx.CollectableRow) (domain.NotebookSummary, error) {
	var s domain.NotebookSummary
	err := row.Scan(&s.ID, &s.Name, &s.CellsCount, &s.UpdatedAt)
	return s, err
}
