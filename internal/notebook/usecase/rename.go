package usecase

import (
	"context"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

func (uc *NotebookUsecase) Rename(ctx context.Context, id, ownerID int64, name string) (domain.Notebook, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.Notebook{}, ErrEmptyName
	}
	return uc.repo.Rename(ctx, id, ownerID, name)
}
