package usecase

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/ipynb"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/models"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/repository"
	"github.com/google/uuid"
)

// NotebookUsecase реализует Usecase поверх базы и хранилища файлов.
type NotebookUsecase struct {
	meta  repository.Metadata
	files repository.Files
	// newID подменяется в тестах, чтобы id были предсказуемыми.
	newID func() string
}

var _ Usecase = (*NotebookUsecase)(nil)

// NewNotebookUsecase выдаёт id блоков и ключи файлов через uuid.
func NewNotebookUsecase(meta repository.Metadata, files repository.Files) *NotebookUsecase {
	return &NotebookUsecase{meta: meta, files: files, newID: uuid.NewString}
}

// List читает только базу и в S3 не ходит.
func (uc *NotebookUsecase) List(ctx context.Context, ownerID int64) ([]models.NotebookSummary, error) {
	return uc.meta.List(ctx, ownerID)
}

// Create записывает файл раньше строки в базе.
func (uc *NotebookUsecase) Create(ctx context.Context, ownerID int64, name string) (models.Notebook, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return models.Notebook{}, ErrEmptyName
	}
	cells := []models.Cell{uc.newCell(models.CellKindCode)}
	data, err := ipynb.Build(cells)
	if err != nil {
		return models.Notebook{}, err
	}
	key := "notebooks/" + uc.newID() + ".ipynb"
	if err = uc.files.Put(ctx, key, data); err != nil {
		return models.Notebook{}, err
	}
	n, err := uc.meta.Create(ctx, models.Notebook{
		OwnerID:    ownerID,
		Name:       name,
		FileKey:    key,
		CellsCount: len(cells),
	})
	if err != nil {
		return models.Notebook{}, err
	}
	n.Cells = cells
	return n, nil
}

// Get проверяет владельца по базе до обращения к S3.
func (uc *NotebookUsecase) Get(ctx context.Context, id, ownerID int64) (models.Notebook, error) {
	n, err := uc.meta.Get(ctx, id, ownerID)
	if errors.Is(err, repository.ErrNotebookNotFound) {
		return models.Notebook{}, ErrNotebookNotFound
	}
	if err != nil {
		return models.Notebook{}, err
	}
	n.Cells, err = uc.readCells(ctx, n.FileKey)
	if err != nil {
		return models.Notebook{}, err
	}
	return n, nil
}

// CreateCell вставляет пустой блок на место index, а без index — в конец.
func (uc *NotebookUsecase) CreateCell(ctx context.Context, id, ownerID int64, kind models.CellKind, index *int) (models.Cell, error) {
	var cell models.Cell
	err := uc.modify(ctx, id, ownerID, func(cells []models.Cell) ([]models.Cell, error) {
		if len(cells) >= MaxCells {
			return nil, ErrTooManyCells
		}
		pos := len(cells)
		if index != nil {
			if *index < 0 || *index > len(cells) {
				return nil, ErrIndexOutOfRange
			}
			pos = *index
		}
		cell = uc.newCell(kind)
		return slices.Insert(cells, pos, cell), nil
	})
	if err != nil {
		return models.Cell{}, err
	}
	return cell, nil
}

// DeleteCell возвращает ErrCellNotFound, если блока с таким index нет.
func (uc *NotebookUsecase) DeleteCell(ctx context.Context, id, ownerID int64, index int) error {
	return uc.modify(ctx, id, ownerID, func(cells []models.Cell) ([]models.Cell, error) {
		if index < 0 || index >= len(cells) {
			return nil, ErrCellNotFound
		}
		return slices.Delete(cells, index, index+1), nil
	})
}

// modify меняет блоки через change, пока строка блокнота заблокирована.
func (uc *NotebookUsecase) modify(ctx context.Context, id, ownerID int64, change func([]models.Cell) ([]models.Cell, error)) error {
	err := uc.meta.Modify(ctx, id, ownerID, func(n models.Notebook) (int, error) {
		cells, err := uc.readCells(ctx, n.FileKey)
		if err != nil {
			return 0, err
		}
		cells, err = change(cells)
		if err != nil {
			return 0, err
		}
		data, err := ipynb.Build(cells)
		if err != nil {
			return 0, err
		}
		if err = uc.files.Put(ctx, n.FileKey, data); err != nil {
			return 0, err
		}
		return len(cells), nil
	})
	if errors.Is(err, repository.ErrNotebookNotFound) {
		return ErrNotebookNotFound
	}
	return err
}

func (uc *NotebookUsecase) readCells(ctx context.Context, key string) ([]models.Cell, error) {
	data, err := uc.files.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	cells, err := ipynb.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", key, err)
	}
	return cells, nil
}

func (uc *NotebookUsecase) newCell(kind models.CellKind) models.Cell {
	return models.Cell{ID: uc.newID(), Kind: kind}
}
