package usecase

import (
	"errors"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

var ErrInvalidList = errors.New("invalid notebook list parameters")

type ListInput struct {
	Search string
	Limit  *int
	Cursor *string
}
type ListPosition struct {
	UpdatedAt time.Time
	ID        int64
}
type ListQuery struct {
	Search string
	Limit  int
	After  *ListPosition
}
type ListRows struct {
	Items        []domain.NotebookSummary
	MatchedCount int64
	TotalCount   int64
}
type ListPage struct {
	Items        []domain.NotebookSummary
	NextCursor   *string
	MatchedCount int64
	TotalCount   int64
}
type ListCursor interface {
	Encode(ownerID int64, search string, position ListPosition) (string, error)
	Decode(token string, ownerID int64, search string) (ListPosition, error)
}
