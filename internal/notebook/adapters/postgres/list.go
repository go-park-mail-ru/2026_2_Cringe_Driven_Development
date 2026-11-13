package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
)

// ListPage reads counts and up to Limit+1 summaries from one database snapshot.
func (r *NotebookRepository) ListPage(ctx context.Context, ownerID int64, q usecase.ListQuery) (usecase.ListRows, error) {
	result := usecase.ListRows{Items: []domain.NotebookSummary{}}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY`); err != nil {
		return result, err
	}
	args := []any{ownerID}
	filter := "TRUE"
	if q.Search != "" {
		pattern := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q.Search)
		args = append(args, "%"+pattern+"%")
		filter = `name ILIKE $2 ESCAPE E'\\'`
	}
	if err = tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE `+filter+`) FROM notebooks WHERE owner_id=$1`, args...).Scan(&result.TotalCount, &result.MatchedCount); err != nil {
		return result, err
	}
	// Reserve stable parameter numbers for the optional search and position.
	pageArgs := []any{ownerID}
	where := "owner_id=$1 AND " + filter
	if q.Search != "" {
		pageArgs = append(pageArgs, args[1])
	}
	if q.After != nil {
		n := len(pageArgs) + 1
		where += fmt.Sprintf(" AND (updated_at,id) < ($%d,$%d)", n, n+1)
		pageArgs = append(pageArgs, q.After.UpdatedAt, q.After.ID)
	}
	pageArgs = append(pageArgs, q.Limit+1)
	query := `SELECT id,name,cells_count,preview_code,updated_at FROM notebooks WHERE ` + where + ` ORDER BY updated_at DESC,id DESC LIMIT $` + strconv.Itoa(len(pageArgs))
	rows, err := tx.Query(ctx, query, pageArgs...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var n domain.NotebookSummary
		if err = rows.Scan(&n.ID, &n.Name, &n.CellsCount, &n.PreviewCode, &n.UpdatedAt); err != nil {
			rows.Close()
			return result, err
		}
		result.Items = append(result.Items, n)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return result, err
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return result, nil
}
