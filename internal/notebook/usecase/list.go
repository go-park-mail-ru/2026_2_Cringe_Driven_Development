package usecase

import (
	"context"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

func (uc *NotebookUsecase) List(ctx context.Context, ownerID int64, input ListInput) (ListPage, error) {
	limit := 24
	if input.Limit != nil {
		limit = *input.Limit
	}
	if limit < 1 || limit > 100 {
		return ListPage{}, ErrInvalidList
	}
	search := strings.TrimSpace(input.Search)
	query := ListQuery{Search: search, Limit: limit}
	if input.Cursor != nil {
		position, err := uc.cursor.Decode(*input.Cursor, ownerID, search)
		if err != nil {
			return ListPage{}, err
		}
		query.After = &position
	}
	rows, err := uc.repo.ListPage(ctx, ownerID, query)
	if err != nil {
		return ListPage{}, err
	}
	page := ListPage{Items: rows.Items, MatchedCount: rows.MatchedCount, TotalCount: rows.TotalCount}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		token, encodeErr := uc.cursor.Encode(ownerID, search, ListPosition{UpdatedAt: last.UpdatedAt, ID: last.ID})
		if encodeErr != nil {
			return ListPage{}, encodeErr
		}
		page.NextCursor = &token
	}
	if page.Items == nil {
		page.Items = []domain.NotebookSummary{}
	}
	active, err := uc.sessions.ActiveNotebook(ctx, ownerID)
	if err != nil {
		return ListPage{}, err
	}
	for i := range page.Items {
		page.Items[i].HasActiveSession = active != 0 && page.Items[i].ID == active
	}
	return page, nil
}
