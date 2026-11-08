// Package usecase содержит бизнес-логику блокнотов.
package usecase

import (
	"context"
	"errors"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

// MaxCells ограничивает число блоков в блокноте.
const MaxCells = 1000

// Лимиты применяются к UTF-8 source и полному сериализованному ipynb в байтах.
const (
	MaxSourceBytes   = 64 * 1024
	MaxNotebookBytes = 128 * 1024 * 1024
)

var (
	ErrTooLarge         = domain.ErrTooLarge
	ErrNotebookConflict = errors.New("notebook content has changed")
	ErrInvalidHash      = errors.New("notebook hash is required")
	ErrInvalidCellKind  = errors.New("cell kind must be code or markdown")
	ErrNotebookNotFound = errors.New("notebook not found")
	ErrNotebookInUse    = errors.New("notebook is in use")
	ErrCellNotFound     = errors.New("cell not found")
	ErrEmptyName        = errors.New("notebook name is empty")
	ErrIndexOutOfRange  = errors.New("cell index is out of range")
	ErrTooManyCells     = errors.New("notebook already has the maximum number of cells")
)

// Usecase работает только с блокнотами владельца.
type Usecase interface {
	Editing
	List(ctx context.Context, ownerID int64) ([]domain.NotebookSummary, error)
	// Create кладёт в новый блокнот один пустой блок кода.
	Create(ctx context.Context, ownerID int64, name string) (domain.Notebook, error)
	Get(ctx context.Context, id, ownerID int64) (domain.Notebook, error)
	// DeleteNotebook removes the notebook only after its runtime work has finished.
	DeleteNotebook(ctx context.Context, id, ownerID int64) error
	// CreateCell вставляет пустой блок на место index, а без index — в конец.
	CreateCell(ctx context.Context, id, ownerID int64, hash string, kind domain.CellKind, index *int) (domain.Cell, error)
	DeleteCell(ctx context.Context, id, ownerID int64, hash string, index int) error
}

// Editing checks the opaque notebook revision before each operation.
type Editing interface {
	Content(ctx context.Context, id, ownerID int64) (domain.Content, error)
	AddCell(ctx context.Context, id, ownerID int64, hash string, kind domain.CellKind, source string, index *int) (domain.CellChange, error)
	EditCell(ctx context.Context, id, ownerID int64, hash, cellID, source string) (domain.CellChange, error)
	MoveCell(ctx context.Context, id, ownerID int64, hash, cellID string, index int) (string, error)
	RemoveCell(ctx context.Context, id, ownerID int64, hash, cellID string) (string, error)
}
