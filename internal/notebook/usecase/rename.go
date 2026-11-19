package usecase

import (
	"context"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

// Rename меняет имя блокнота и возвращает обновлённые метаданные.
func (uc *NotebookUsecase) Rename(ctx context.Context, id, ownerID int64, name string) (domain.Notebook, error) {
	name, err := NormalizeName(name)
	if err != nil {
		return domain.Notebook{}, err
	}
	n, err := uc.repo.Rename(ctx, id, ownerID, name)
	if err != nil {
		return domain.Notebook{}, err
	}
	n.Access = domain.AccessFor(n.OwnerID, ownerID)
	return n, nil
}
