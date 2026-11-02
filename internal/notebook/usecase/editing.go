package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/google/uuid"
)

func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Content читает неизменяемый объект по согласованной ссылке из PostgreSQL.
func (uc *NotebookUsecase) Content(ctx context.Context, id, ownerID int64) (domain.Content, error) {
	n, err := uc.meta.Get(ctx, id, ownerID)
	if err != nil {
		return domain.Content{}, err
	}
	data, err := uc.files.Get(ctx, n.FileKey)
	if err != nil {
		return domain.Content{}, err
	}
	if len(data) > MaxNotebookBytes {
		return domain.Content{}, ErrTooLarge
	}
	cells, err := uc.codec.Parse(data)
	if err != nil {
		return domain.Content{}, err
	}
	return domain.Content{Hash: contentHash(data), Cells: cells}, nil
}

func (uc *NotebookUsecase) AddCell(ctx context.Context, id, ownerID int64, hash string, kind domain.CellKind, source string, index *int) (domain.CellChange, error) {
	if len(source) > MaxSourceBytes {
		return domain.CellChange{}, ErrTooLarge
	}
	var cell domain.Cell
	result, err := uc.changeCells(ctx, id, ownerID, hash, func(cells []domain.Cell) ([]domain.Cell, error) {
		if kind != domain.CellKindCode && kind != domain.CellKindMarkdown {
			return nil, ErrInvalidCellKind
		}
		if len(cells) >= MaxCells {
			return nil, ErrTooManyCells
		}
		pos := len(cells)
		if index != nil {
			pos = *index
		}
		if pos < 0 || pos > len(cells) {
			return nil, ErrIndexOutOfRange
		}
		cell = uc.newCell(kind)
		cell.Source = source
		return slices.Insert(cells, pos, cell), nil
	})
	if err != nil {
		return domain.CellChange{}, err
	}
	return domain.CellChange{Hash: result.Hash, Cell: cell}, nil
}

func (uc *NotebookUsecase) EditCell(ctx context.Context, id, ownerID int64, hash, cellID, source string) (domain.CellChange, error) {
	if len(source) > MaxSourceBytes {
		return domain.CellChange{}, ErrTooLarge
	}
	var cell domain.Cell
	result, err := uc.changeCells(ctx, id, ownerID, hash, func(cells []domain.Cell) ([]domain.Cell, error) {
		index := cellIndex(cells, cellID)
		if index < 0 {
			return nil, ErrCellNotFound
		}
		cells[index].Source = source
		cell = cells[index]
		return cells, nil
	})
	if err != nil {
		return domain.CellChange{}, err
	}
	return domain.CellChange{Hash: result.Hash, Cell: cell}, nil
}

func (uc *NotebookUsecase) MoveCell(ctx context.Context, id, ownerID int64, hash, cellID string, index int) (string, error) {
	result, err := uc.changeCells(ctx, id, ownerID, hash, func(cells []domain.Cell) ([]domain.Cell, error) {
		from := cellIndex(cells, cellID)
		if from < 0 {
			return nil, ErrCellNotFound
		}
		if index < 0 || index >= len(cells) {
			return nil, ErrIndexOutOfRange
		}
		if index == from {
			return cells, nil
		}
		cell := cells[from]
		cells = slices.Delete(cells, from, from+1)
		return slices.Insert(cells, index, cell), nil
	})
	return result.Hash, err
}

func (uc *NotebookUsecase) RemoveCell(ctx context.Context, id, ownerID int64, hash, cellID string) (string, error) {
	result, err := uc.changeCells(ctx, id, ownerID, hash, func(cells []domain.Cell) ([]domain.Cell, error) {
		index := cellIndex(cells, cellID)
		if index < 0 {
			return nil, ErrCellNotFound
		}
		return deleteCellAt(cells, index)
	})
	return result.Hash, err
}

func cellIndex(cells []domain.Cell, id string) int {
	return slices.IndexFunc(cells, func(c domain.Cell) bool { return c.ID == id })
}

// changeCells проверяет редакцию и сериализует все записи блокировкой строки БД.
// Новый объект загружается до коммита; старый не изменяется и не удаляется.
func (uc *NotebookUsecase) changeCells(ctx context.Context, id, ownerID int64, expected string, change func([]domain.Cell) ([]domain.Cell, error)) (domain.Content, error) {
	var result domain.Content
	err := uc.meta.Modify(ctx, id, ownerID, func(n domain.Notebook) (MetadataUpdate, error) {
		if expected == "" {
			return MetadataUpdate{}, ErrInvalidHash
		}
		data, err := uc.files.Get(ctx, n.FileKey)
		if err != nil {
			return MetadataUpdate{}, err
		}
		if len(data) > MaxNotebookBytes {
			return MetadataUpdate{}, ErrTooLarge
		}
		hash := contentHash(data)
		if expected != hash {
			return MetadataUpdate{}, ErrNotebookConflict
		}
		cells, err := uc.codec.Parse(data)
		if err != nil {
			return MetadataUpdate{}, err
		}
		original := slices.Clone(cells)
		cells, err = change(cells)
		if err != nil {
			return MetadataUpdate{}, err
		}
		if slices.Equal(original, cells) {
			result = domain.Content{Hash: hash, Cells: cells}
			return MetadataUpdate{}, nil
		}
		updated, err := uc.codec.Rewrite(data, cells)
		if err != nil {
			return MetadataUpdate{}, err
		}
		if len(updated) > MaxNotebookBytes {
			return MetadataUpdate{}, ErrTooLarge
		}
		key := "notebooks/" + uuid.NewString() + ".ipynb"
		if err = uc.files.Put(ctx, key, updated); err != nil {
			return MetadataUpdate{}, err
		}
		result = domain.Content{Hash: contentHash(updated), Cells: cells}
		return MetadataUpdate{FileKey: key, CellsCount: len(cells), Changed: true}, nil
	})
	if err != nil {
		return domain.Content{}, err
	}
	return result, nil
}
