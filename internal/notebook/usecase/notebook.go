package usecase

import (
	"context"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/google/uuid"
)

type NotebookUsecase struct {
	repo     Repository
	sessions ActiveSessions
	newID    func() string
}

func NewNotebookUsecase(repo Repository, sessions ActiveSessions) *NotebookUsecase {
	return &NotebookUsecase{repo: repo, sessions: sessions, newID: uuid.NewString}
}
func (uc *NotebookUsecase) List(ctx context.Context, ownerID int64) ([]domain.NotebookSummary, error) {
	list, err := uc.repo.List(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	active, err := uc.sessions.ActiveNotebook(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].HasActiveSession = active != 0 && list[i].ID == active
	}
	return list, nil
}
func (uc *NotebookUsecase) Create(ctx context.Context, ownerID int64, name string) (domain.Notebook, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.Notebook{}, ErrEmptyName
	}
	return uc.repo.Create(ctx, domain.Notebook{OwnerID: ownerID, Name: name, Document: domain.Document{NBFormat: 4, NBFormatMinor: 5, Metadata: []byte(`{}`)}, Cells: []domain.Cell{{ID: uc.newID(), Kind: domain.CellKindCode, Metadata: []byte(`{}`), Outputs: []byte(`[]`), HasExecutionCount: true}}})
}
func (uc *NotebookUsecase) Get(ctx context.Context, id, ownerID int64) (domain.Notebook, error) {
	return uc.repo.Get(ctx, id, ownerID)
}
func (uc *NotebookUsecase) Content(ctx context.Context, id, ownerID int64) (domain.Content, error) {
	n, err := uc.repo.Get(ctx, id, ownerID)
	if err != nil {
		return domain.Content{}, err
	}
	return domain.Content{Hash: n.Revision, Cells: n.Cells}, nil
}
func (uc *NotebookUsecase) CreateCell(ctx context.Context, id, ownerID int64, hash string, kind domain.CellKind, index *int) (domain.Cell, error) {
	r, err := uc.AddCell(ctx, id, ownerID, hash, kind, "", index)
	return r.Cell, err
}
func (uc *NotebookUsecase) DeleteCell(ctx context.Context, id, ownerID int64, hash string, index int) error {
	_, err := uc.repo.Apply(ctx, id, ownerID, hash, Operation{Kind: Delete, Index: &index})
	return err
}

// DeleteNotebook delegates the ownership and runtime checks to one storage transaction.
func (uc *NotebookUsecase) DeleteNotebook(ctx context.Context, id, ownerID int64) error {
	return uc.repo.DeleteNotebook(ctx, id, ownerID)
}
