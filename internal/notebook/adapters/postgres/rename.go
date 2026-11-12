package postgres

import (
	"context"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

// Rename сохраняет только имя и дату под общей блокировкой блокнота.
func (r *NotebookRepository) Rename(ctx context.Context, id, ownerID int64, name string) (domain.Notebook, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Notebook{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	n, err := scanNotebook(tx.QueryRow(ctx, selectNotebook+` FOR UPDATE`, id, ownerID))
	if err != nil {
		return domain.Notebook{}, err
	}
	if n.Name != name {
		err = tx.QueryRow(ctx, `UPDATE notebooks SET name=$3,updated_at=clock_timestamp() WHERE id=$1 AND owner_id=$2 RETURNING updated_at`, id, ownerID, name).Scan(&n.UpdatedAt)
		if err != nil {
			return domain.Notebook{}, err
		}
		n.Name = name
	}
	n, err = readContent(ctx, tx, n)
	if err != nil {
		return domain.Notebook{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Notebook{}, err
	}
	return n, nil
}
