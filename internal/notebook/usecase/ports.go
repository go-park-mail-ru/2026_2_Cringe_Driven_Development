package usecase

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

var ErrFileNotFound = errors.New("notebook file not found")
var ErrStaleSource = errors.New("executed source is no longer current")
var ErrInvalidDocument = errors.New("invalid notebook data")

type OperationKind string

const (
	Insert OperationKind = "insert"
	Edit   OperationKind = "edit"
	Move   OperationKind = "move"
	Delete OperationKind = "delete"
)

// Operation передаёт только запрошенное изменение; Index выбирает legacy-операции
// только после проверки ревизии ноутбука под блокировкой его строки.
type Operation struct {
	Kind     OperationKind
	CellID   string
	CellKind domain.CellKind
	Source   string
	Index    *int
}
type Change struct {
	Hash string
	Cell domain.Cell
}
type Repository interface {
	Create(context.Context, domain.Notebook) (domain.Notebook, error)
	ListPage(context.Context, int64, ListQuery) (ListRows, error)
	Get(context.Context, int64, int64) (domain.Notebook, error)
	Rename(context.Context, int64, int64, string) (domain.Notebook, error)
	DeleteNotebook(context.Context, int64, int64) error
	Apply(context.Context, int64, int64, string, Operation) (Change, error)
}

// ActiveSessions поставляется из #13. Нулевой ID ноутбука означает отсутствие активной сессии.
// Не связано с сессиями аутентификации и подготовленными окружениями.
type ActiveSessions interface {
	ActiveNotebook(context.Context, int64) (int64, error)
}

// NoActiveSessions нужен для автономных тестов ноутбуков; app использует хранилище runtime-сессий.
type NoActiveSessions struct{}

func (NoActiveSessions) ActiveNotebook(context.Context, int64) (int64, error) { return 0, nil }

// ExecutionSource привязывает отправленное выполнение к стабильной ячейке и версии исходника.
type ExecutionSource struct{ CellID, Source, SourceVersion string }
type CellResult struct {
	OutputTruncated       bool
	CellID, SourceVersion string
	Outputs               json.RawMessage
	ExecutionCount        *int64
}
type Results interface {
	ExecutionSource(context.Context, int64, int64, string) (ExecutionSource, error)
	SaveResult(context.Context, int64, int64, CellResult) (string, error)
}
