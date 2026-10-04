// Package postgres хранит метаданные блокнотов в PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/models"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/repository"
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

// NotebookRepository реализует repository.Metadata на таблице notebooks.
type NotebookRepository struct {
	db DB
}

var _ repository.Metadata = (*NotebookRepository)(nil)

// NewNotebookRepository принимает пул соединений.
func NewNotebookRepository(db DB) *NotebookRepository {
	return &NotebookRepository{db: db}
}

// Create вставляет строку; id и время создания выдаёт база.
func (r *NotebookRepository) Create(ctx context.Context, n models.Notebook) (models.Notebook, error) {
	err := r.db.QueryRow(ctx,
		`INSERT INTO notebooks (owner_id, name, file_key, cells_count) VALUES ($1, $2, $3, $4)
		RETURNING id, created_at, updated_at`,
		n.OwnerID, n.Name, n.FileKey, n.CellsCount,
	).Scan(&n.ID, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		return models.Notebook{}, fmt.Errorf("insert notebook: %w", err)
	}
	return n, nil
}

// List отдаёт блокноты владельца, сначала недавно изменённые.
func (r *NotebookRepository) List(ctx context.Context, ownerID int64) ([]models.NotebookSummary, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, name, cells_count, updated_at FROM notebooks
		WHERE owner_id = $1 ORDER BY updated_at DESC`,
		ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("select notebooks: %w", err)
	}
	list, err := pgx.CollectRows(rows, scanSummary)
	if err != nil {
		return nil, fmt.Errorf("scan notebooks: %w", err)
	}
	return list, nil
}

// Get ищет блокнот по id только среди блокнотов владельца.
func (r *NotebookRepository) Get(ctx context.Context, id, ownerID int64) (models.Notebook, error) {
	return scanNotebook(r.db.QueryRow(ctx, selectNotebook, id, ownerID))
}

// Modify выполняет fn под SELECT ... FOR UPDATE и сохраняет новое число блоков.
func (r *NotebookRepository) Modify(ctx context.Context, id, ownerID int64, fn func(models.Notebook) (int, error)) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	n, err := scanNotebook(tx.QueryRow(ctx, selectNotebook+` FOR UPDATE`, id, ownerID))
	if err != nil {
		return err
	}
	cellsCount, err := fn(n)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`UPDATE notebooks SET cells_count = $2, updated_at = now() WHERE id = $1`,
		id, cellsCount,
	)
	if err != nil {
		return fmt.Errorf("update notebook: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func scanNotebook(row pgx.Row) (models.Notebook, error) {
	var n models.Notebook
	err := row.Scan(&n.ID, &n.OwnerID, &n.Name, &n.FileKey, &n.CellsCount, &n.CreatedAt, &n.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.Notebook{}, repository.ErrNotebookNotFound
	}
	if err != nil {
		return models.Notebook{}, fmt.Errorf("select notebook: %w", err)
	}
	return n, nil
}

func scanSummary(row pgx.CollectableRow) (models.NotebookSummary, error) {
	var s models.NotebookSummary
	err := row.Scan(&s.ID, &s.Name, &s.CellsCount, &s.UpdatedAt)
	return s, err
}
