package usecase

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

func ValidateSource(source string) error {
	if len(source) > MaxSourceBytes {
		return ErrTooLarge
	}
	if !utf8.ValidString(source) || strings.ContainsRune(source, 0) {
		return ErrInvalidDocument
	}
	return nil
}
func (uc *NotebookUsecase) AddCell(ctx context.Context, id, ownerID int64, hash string, kind domain.CellKind, source string, index *int) (domain.CellChange, error) {
	if err := ValidateSource(source); err != nil {
		return domain.CellChange{}, err
	}
	if kind != domain.CellKindCode && kind != domain.CellKindMarkdown {
		return domain.CellChange{}, ErrInvalidCellKind
	}
	r, err := uc.repo.Apply(ctx, id, ownerID, hash, Operation{Kind: Insert, CellID: uc.newID(), CellKind: kind, Source: source, Index: index})
	return domain.CellChange{Hash: r.Hash, Cell: r.Cell}, err
}
func (uc *NotebookUsecase) EditCell(ctx context.Context, id, ownerID int64, hash, cellID, source string) (domain.CellChange, error) {
	if err := ValidateSource(source); err != nil {
		return domain.CellChange{}, err
	}
	r, err := uc.repo.Apply(ctx, id, ownerID, hash, Operation{Kind: Edit, CellID: cellID, Source: source})
	return domain.CellChange{Hash: r.Hash, Cell: r.Cell}, err
}
func (uc *NotebookUsecase) MoveCell(ctx context.Context, id, ownerID int64, hash, cellID string, index int) (string, error) {
	r, err := uc.repo.Apply(ctx, id, ownerID, hash, Operation{Kind: Move, CellID: cellID, Index: &index})
	return r.Hash, err
}
func (uc *NotebookUsecase) RemoveCell(ctx context.Context, id, ownerID int64, hash, cellID string) (string, error) {
	r, err := uc.repo.Apply(ctx, id, ownerID, hash, Operation{Kind: Delete, CellID: cellID})
	return r.Hash, err
}
