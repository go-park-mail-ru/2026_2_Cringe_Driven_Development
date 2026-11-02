package usecase

import (
	"context"
	"errors"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

var (
	ErrFileNotFound = errors.New("notebook file not found")
)

// Metadata хранит строки блокнотов; чужой блокнот для неё — ErrNotebookNotFound.
// MetadataUpdate описывает переход на уже записанный неизменяемый объект.
// Changed=false оставляет метаданные, включая updated_at, без изменений.
type MetadataUpdate struct {
	FileKey    string
	CellsCount int
	Changed    bool
}

type Metadata interface {
	// Create заполняет у n поля ID, CreatedAt и UpdatedAt.
	Create(ctx context.Context, n domain.Notebook) (domain.Notebook, error)
	List(ctx context.Context, ownerID int64) ([]domain.NotebookSummary, error)
	Get(ctx context.Context, id, ownerID int64) (domain.Notebook, error)
	// Modify блокирует строку до завершения fn и атомарно сохраняет новую редакцию.
	Modify(ctx context.Context, id, ownerID int64, fn func(n domain.Notebook) (MetadataUpdate, error)) error
}

// Files хранит файлы .ipynb по ключу.
type Files interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, data []byte) error
}

// NotebookCodec отделяет формат ipynb от сценариев блокнота.
type NotebookCodec interface {
	Parse(data []byte) ([]domain.Cell, error)
	Build(cells []domain.Cell) ([]byte, error)
	Rewrite(data []byte, cells []domain.Cell) ([]byte, error)
}
