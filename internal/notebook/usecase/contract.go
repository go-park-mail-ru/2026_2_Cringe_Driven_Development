// Package usecase содержит бизнес-логику блокнотов.
package usecase

import (
	"context"
	"errors"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/models"
)

// MaxCells ограничивает число блоков в блокноте.
const MaxCells = 1000

var (
	ErrNotebookNotFound = errors.New("notebook not found")
	ErrCellNotFound     = errors.New("cell not found")
	ErrEmptyName        = errors.New("notebook name is empty")
	ErrIndexOutOfRange  = errors.New("cell index is out of range")
	ErrTooManyCells     = errors.New("notebook already has the maximum number of cells")
)

// Usecase работает только с блокнотами владельца.
type Usecase interface {
	List(ctx context.Context, ownerID int64) ([]models.NotebookSummary, error)
	// Create кладёт в новый блокнот один пустой блок кода.
	Create(ctx context.Context, ownerID int64, name string) (models.Notebook, error)
	Get(ctx context.Context, id, ownerID int64) (models.Notebook, error)
	// CreateCell вставляет пустой блок на место index, а без index — в конец.
	CreateCell(ctx context.Context, id, ownerID int64, kind models.CellKind, index *int) (models.Cell, error)
	DeleteCell(ctx context.Context, id, ownerID int64, index int) error
}
