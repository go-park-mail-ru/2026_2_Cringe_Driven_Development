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
var ErrMigrationRequired = errors.New("notebook migration required")

type OperationKind string

const (
	Insert OperationKind = "insert"
	Edit   OperationKind = "edit"
	Move   OperationKind = "move"
	Delete OperationKind = "delete"
)

// Operation transmits only the requested change; Index selects legacy operations
// only after the notebook revision has been checked under its row lock.
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
	List(context.Context, int64) ([]domain.NotebookSummary, error)
	Get(context.Context, int64, int64) (domain.Notebook, error)
	Apply(context.Context, int64, int64, string, Operation) (Change, error)
}

// ActiveSessions is supplied by #13. Notebook ID zero means no active session.
// It is unrelated to authentication sessions and provisioned environments.
type ActiveSessions interface {
	ActiveNotebook(context.Context, int64) (int64, error)
}

// NoActiveSessions supports standalone notebook tests; app uses the runtime session store.
type NoActiveSessions struct{}

func (NoActiveSessions) ActiveNotebook(context.Context, int64) (int64, error) { return 0, nil }

// ExecutionSource binds a dispatched execution to a stable cell and source version.
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
