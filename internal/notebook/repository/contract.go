// Package repository описывает хранилища блокнотов: метаданные и файлы .ipynb.
package repository

import (
	"context"
	"errors"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/models"
)

var (
	ErrNotebookNotFound = errors.New("notebook not found")
	ErrFileNotFound     = errors.New("notebook file not found")
)

// Metadata хранит строки блокнотов; чужой блокнот для неё — ErrNotebookNotFound.
type Metadata interface {
	// Create заполняет у n поля ID, CreatedAt и UpdatedAt.
	Create(ctx context.Context, n models.Notebook) (models.Notebook, error)
	List(ctx context.Context, ownerID int64) ([]models.NotebookSummary, error)
	Get(ctx context.Context, id, ownerID int64) (models.Notebook, error)
	// Modify блокирует строку блокнота на время fn и сохраняет число блоков, которое вернула fn.
	Modify(ctx context.Context, id, ownerID int64, fn func(n models.Notebook) (cellsCount int, err error)) error
}

// Files хранит файлы .ipynb по ключу.
type Files interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, data []byte) error
}
