package usecase

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

// ImportDecoder проверяет IPYNB и возвращает документ, ячейки и число пропусков.
// Ошибки валидации декодера должны быть пригодны для ответа клиенту.
type ImportDecoder func([]byte) (domain.Document, []domain.Cell, int, error)

// WithImportDecoder подключает декодер загружаемых документов.
func WithImportDecoder(decode ImportDecoder) Option {
	return func(uc *NotebookUsecase) { uc.decodeImport = decode }
}

// Import создаёт частный блокнот для ownerID из raw и возвращает число пропусков.
func (uc *NotebookUsecase) Import(ctx context.Context, ownerID int64, name string, raw []byte) (domain.Notebook, int, error) {
	var empty domain.Notebook
	if utf8.RuneCountInString(name) > 128 {
		return empty, 0, ErrInvalidName
	}
	name, err := NormalizeName(name)
	if err != nil {
		return empty, 0, err
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return empty, 0, contextErr
	}
	if uc.decodeImport == nil {
		return empty, 0, errors.New("notebook import decoder is unavailable")
	}
	doc, cells, skipped, err := uc.decodeImport(raw)
	if err != nil {
		return empty, 0, fmt.Errorf("%w: %w", ErrInvalidDocument, err)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return empty, 0, contextErr
	}
	n, err := uc.repo.Create(ctx, domain.Notebook{OwnerID: ownerID, Name: name, Document: doc, Cells: cells})
	if err != nil {
		return empty, 0, err
	}
	n.Access = domain.AccessFor(n.OwnerID, ownerID)
	return n, skipped, nil
}
