package postgres

import (
	"context"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
)

// SuggestUsers performs indexed literal prefix search and excludes the caller.
func (r *UserRepository) SuggestUsers(ctx context.Context, userID int64, q string, limit int) ([]domain.UserSuggestion, error) {
	pattern := strings.ReplaceAll(strings.ToLower(q), "_", `\_`) + "%"
	rows, err := r.db.Query(ctx, `SELECT id,login FROM users WHERE lower(login) COLLATE "C" LIKE $1 ESCAPE '\' AND id <> $2 ORDER BY lower(login) COLLATE "C",id LIMIT $3`, pattern, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.UserSuggestion, 0)
	for rows.Next() {
		var u domain.UserSuggestion
		if err = rows.Scan(&u.ID, &u.Login); err != nil {
			return nil, err
		}
		items = append(items, u)
	}
	return items, rows.Err()
}
