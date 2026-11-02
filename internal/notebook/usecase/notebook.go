package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/google/uuid"
)

// NotebookUsecase реализует Usecase поверх базы и хранилища файлов.
type NotebookUsecase struct {
	codec NotebookCodec
	meta  Metadata
	files Files
	// newID подменяется в тестах, чтобы id были предсказуемыми.
	newID func() string
}

// NewNotebookUsecase выдаёт id блоков и ключи файлов через uuid.
func NewNotebookUsecase(meta Metadata, files Files, codec NotebookCodec) *NotebookUsecase {
	return &NotebookUsecase{meta: meta, files: files, codec: codec, newID: uuid.NewString}
}

// List читает только базу и в S3 не ходит.
func (uc *NotebookUsecase) List(ctx context.Context, ownerID int64) ([]domain.NotebookSummary, error) {
	return uc.meta.List(ctx, ownerID)
}

// Create записывает файл раньше строки в базе.
func (uc *NotebookUsecase) Create(ctx context.Context, ownerID int64, name string) (domain.Notebook, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.Notebook{}, ErrEmptyName
	}
	cells := []domain.Cell{uc.newCell(domain.CellKindCode)}
	data, err := uc.codec.Build(cells)
	if err != nil {
		return domain.Notebook{}, err
	}
	key := "notebooks/" + uc.newID() + ".ipynb"
	if err = uc.files.Put(ctx, key, data); err != nil {
		return domain.Notebook{}, err
	}
	n, err := uc.meta.Create(ctx, domain.Notebook{
		OwnerID:    ownerID,
		Name:       name,
		FileKey:    key,
		CellsCount: len(cells),
	})
	if err != nil {
		return domain.Notebook{}, err
	}
	n.Cells = cells
	slog.InfoContext(ctx, "notebook.create", slog.String("outcome", "success"), slog.Int64("notebook_id", n.ID))
	return n, nil
}

// Get проверяет владельца по базе до обращения к S3.
func (uc *NotebookUsecase) Get(ctx context.Context, id, ownerID int64) (domain.Notebook, error) {
	n, err := uc.meta.Get(ctx, id, ownerID)
	if errors.Is(err, ErrNotebookNotFound) {
		return domain.Notebook{}, ErrNotebookNotFound
	}
	if err != nil {
		return domain.Notebook{}, err
	}
	n.Cells, err = uc.readCells(ctx, n.FileKey)
	if err != nil {
		return domain.Notebook{}, err
	}
	return n, nil
}

// CreateCell использует общую операцию вставки с пустым source.
func (uc *NotebookUsecase) CreateCell(ctx context.Context, id, ownerID int64, hash string, kind domain.CellKind, index *int) (domain.Cell, error) {
	result, err := uc.AddCell(ctx, id, ownerID, hash, kind, "", index)
	if err != nil {
		return domain.Cell{}, err
	}
	slog.InfoContext(ctx, "notebook.cell_create", slog.String("outcome", "success"), slog.Int64("notebook_id", id))
	return result.Cell, nil
}

// DeleteCell проверяет редакцию прежде, чем интерпретировать index.
func (uc *NotebookUsecase) DeleteCell(ctx context.Context, id, ownerID int64, hash string, index int) error {
	_, err := uc.changeCells(ctx, id, ownerID, hash, func(cells []domain.Cell) ([]domain.Cell, error) { return deleteCellAt(cells, index) })
	if err == nil {
		slog.InfoContext(ctx, "notebook.cell_delete", slog.String("outcome", "success"), slog.Int64("notebook_id", id))
	}
	return err
}

func deleteCellAt(cells []domain.Cell, index int) ([]domain.Cell, error) {
	if index < 0 || index >= len(cells) {
		return nil, ErrCellNotFound
	}
	return slices.Delete(cells, index, index+1), nil
}

func (uc *NotebookUsecase) readCells(ctx context.Context, key string) ([]domain.Cell, error) {
	data, err := uc.files.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxNotebookBytes {
		return nil, ErrTooLarge
	}
	cells, err := uc.codec.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", key, err)
	}
	return cells, nil
}

func (uc *NotebookUsecase) newCell(kind domain.CellKind) domain.Cell {
	return domain.Cell{ID: uc.newID(), Kind: kind}
}
