package postgres

import (
	"context"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/jackc/pgx/v5"
)

func (r *NotebookRepository) PutSharing(ctx context.Context, id, ownerID int64, mode domain.SharingMode, logins []string) (domain.SharingSettings, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.SharingSettings{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = scanNotebook(tx.QueryRow(ctx, selectNotebook+` FOR UPDATE`, id, ownerID)); err != nil {
		return domain.SharingSettings{}, err
	}
	if err = usecase.ValidateSharing(mode, logins); err != nil {
		return domain.SharingSettings{}, err
	}
	normalized := make([]string, 0, len(logins))
	unique := map[string]bool{}
	for _, login := range logins {
		login = strings.ToLower(login)
		if !unique[login] {
			unique[login] = true
			normalized = append(normalized, login)
		}
	}
	readers := make([]domain.NotebookReader, 0, len(normalized))
	if len(normalized) > 0 {
		rows, queryErr := tx.Query(ctx, `SELECT id,login FROM users WHERE lower(login)=ANY($1::text[]) ORDER BY lower(login),id FOR KEY SHARE`, normalized)
		if queryErr != nil {
			return domain.SharingSettings{}, queryErr
		}
		matched := 0
		for rows.Next() {
			var reader domain.NotebookReader
			if err = rows.Scan(&reader.ID, &reader.Login); err != nil {
				rows.Close()
				return domain.SharingSettings{}, err
			}
			matched++
			if reader.ID != ownerID {
				readers = append(readers, reader)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return domain.SharingSettings{}, err
		}
		if matched != len(normalized) {
			return domain.SharingSettings{}, usecase.ErrInvalidSharing
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE notebooks SET sharing_mode=$2 WHERE id=$1`, id, mode); err != nil {
		return domain.SharingSettings{}, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM notebook_readers WHERE notebook_id=$1`, id); err != nil {
		return domain.SharingSettings{}, err
	}
	for _, reader := range readers {
		if _, err = tx.Exec(ctx, `INSERT INTO notebook_readers(notebook_id,user_id) VALUES($1,$2)`, id, reader.ID); err != nil {
			return domain.SharingSettings{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.SharingSettings{}, err
	}
	return domain.SharingSettings{Mode: mode, Readers: readers}, nil
}

func (r *NotebookRepository) GetSharing(ctx context.Context, id, ownerID int64) (domain.SharingSettings, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.SharingSettings{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx,
		`SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY`,
	)
	if err != nil {
		return domain.SharingSettings{}, err
	}

	n, err := scanNotebook(tx.QueryRow(ctx, selectNotebook, id, ownerID))
	if err != nil {
		return domain.SharingSettings{}, err
	}

	settings := domain.SharingSettings{
		Mode:    n.SharingMode,
		Readers: make([]domain.NotebookReader, 0),
	}

	settings.Readers, err = readReaders(ctx, tx, id)
	if err != nil {
		return domain.SharingSettings{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		return domain.SharingSettings{}, err
	}

	return settings, nil
}

func readReaders(ctx context.Context, tx pgx.Tx, id int64) ([]domain.NotebookReader, error) {
	settings := domain.SharingSettings{Readers: make([]domain.NotebookReader, 0)}
	rows, err := tx.Query(ctx, `
		SELECT u.id, u.login
		FROM notebook_readers AS nr
		JOIN users AS u ON u.id = nr.user_id
		WHERE nr.notebook_id = $1
		ORDER BY lower(u.login), u.id
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var reader domain.NotebookReader
		if err = rows.Scan(&reader.ID, &reader.Login); err != nil {
			return nil, err
		}
		settings.Readers = append(settings.Readers, reader)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	return settings.Readers, nil
}
