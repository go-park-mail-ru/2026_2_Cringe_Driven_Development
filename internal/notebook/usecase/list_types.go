package usecase

import (
	"errors"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

var ErrInvalidList = errors.New("invalid notebook list parameters")

// ListInput передаёт фильтр, лимит и непрозрачный курсор из запроса.
type ListInput struct {
	Search string
	Limit  *int
	Cursor *string
}

// ListPosition хранит время обновления и ID для устойчивой границы страницы.
type ListPosition struct {
	UpdatedAt time.Time
	ID        int64
}

// ListQuery передаёт проверенные параметры чтения страницы в репозиторий.
type ListQuery struct {
	Search string
	Limit  int
	After  *ListPosition
}

// ListRows содержит строки и счётчики, прочитанные одной операцией репозитория.
type ListRows struct {
	Items        []domain.NotebookSummary
	MatchedCount int64
	TotalCount   int64
}

// ListPage содержит страницу результатов, счётчики и курсор продолжения.
type ListPage struct {
	Items        []domain.NotebookSummary
	NextCursor   *string
	MatchedCount int64
	TotalCount   int64
}

// ListCursor задаёт подпись и проверку курсора списка в рамках владельца и фильтра.
type ListCursor interface {
	Encode(ownerID int64, search string, position ListPosition) (string, error)
	Decode(token string, ownerID int64, search string) (ListPosition, error)
}
