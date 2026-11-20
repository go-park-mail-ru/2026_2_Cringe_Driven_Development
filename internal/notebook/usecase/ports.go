package usecase

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

var ErrStaleSource = errors.New("executed source is no longer current")
var ErrInvalidDocument = errors.New("invalid notebook data")

// OperationKind обозначает вставку, редактирование, перемещение или удаление ячейки.
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

// Change возвращает новую редакцию и ячейку, если её изменяла операция.
type Change struct {
	Hash string
	Cell domain.Cell
}

// Repository задаёт операции хранения блокнотов и проверки редакций.
type Repository interface {
	Create(context.Context, domain.Notebook) (domain.Notebook, error)
	ListPage(context.Context, int64, ListQuery) (ListRows, error)
	Get(context.Context, int64, int64) (domain.Notebook, error)
	GetReadable(ctx context.Context, id, userID int64) (domain.Notebook, error)
	CheckRead(ctx context.Context, id, userID int64) error
	PutSharing(ctx context.Context, id, ownerID int64, mode domain.SharingMode, readerLogins []string) (domain.SharingSettings, error)
	Clone(ctx context.Context, id, userID int64, name string) (domain.Notebook, error)
	GetSharing(ctx context.Context, id, ownerID int64) (domain.SharingSettings, error)
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

// ActiveNotebook возвращает нулевой ID блокнота для автономных сценариев.
func (NoActiveSessions) ActiveNotebook(context.Context, int64) (int64, error) { return 0, nil }

// ExecutionSource привязывает отправленное выполнение к стабильной ячейке и версии исходника.
type ExecutionSource struct{ CellID, Source, SourceVersion string }

// CellResult передаёт данные исполнения вместе с версией исходника ячейки.
type CellResult struct {
	OutputTruncated       bool
	CellID, SourceVersion string
	Outputs               json.RawMessage
	ExecutionCount        *int64
}

// Results задаёт чтение исходника и сохранение результата с проверкой его актуальности.
type Results interface {
	ExecutionSource(context.Context, int64, int64, string) (ExecutionSource, error)
	SaveResult(context.Context, int64, int64, CellResult) (string, error)
}
