package usecase

import (
	"context"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/google/uuid"
)

// NotebookUsecase выполняет пользовательские операции с блокнотами через порты.
type NotebookUsecase struct {
	repo     Repository
	sessions ActiveSessions
	newID    func() string
	cursor   ListCursor
}

// NewNotebookUsecase связывает репозиторий repo, источник активных сессий sessions
// и кодек списка cursor. Идентификаторы ячеек создаются локально.
func NewNotebookUsecase(repo Repository, sessions ActiveSessions, cursor ListCursor) *NotebookUsecase {
	return &NotebookUsecase{repo: repo, sessions: sessions, cursor: cursor, newID: uuid.NewString}
}

// Create создаёт блокнот с именем name и начальной ячейкой кода для владельца ownerID.
// Контекст ctx определяет срок записи. Пустое после обрезки имя отклоняется.
func (uc *NotebookUsecase) Create(ctx context.Context, ownerID int64, name string) (domain.Notebook, error) {
	var err error
	name, err = NormalizeName(name)
	if err != nil {
		return domain.Notebook{}, err
	}
	n, err := uc.repo.Create(ctx, domain.Notebook{
		OwnerID: ownerID,
		Name:    name,
		Document: domain.Document{
			NBFormat:      4,
			NBFormatMinor: 5,
			Metadata:      []byte(`{}`),
		},
		Cells: []domain.Cell{{
			ID:                uc.newID(),
			Kind:              domain.CellKindCode,
			Metadata:          []byte(`{}`),
			Outputs:           []byte(`[]`),
			HasExecutionCount: true,
		}},
	})
	if err != nil {
		return domain.Notebook{}, err
	}
	n.Access = domain.AccessFor(n.OwnerID, ownerID)
	return n, nil
}

// Get читает доступный блокнот; ownerID обозначает посетителя, 0 — анонима.
func (uc *NotebookUsecase) Get(ctx context.Context, id, ownerID int64) (domain.Notebook, error) {
	n, err := uc.repo.GetReadable(ctx, id, ownerID)
	if err != nil {
		return domain.Notebook{}, err
	}
	n.Access = domain.AccessFor(n.OwnerID, ownerID)
	return n, nil
}

// Content возвращает согласованный снимок доступного посетителю блокнота.
func (uc *NotebookUsecase) Content(ctx context.Context, id, ownerID int64) (domain.Content, error) {
	n, err := uc.repo.GetReadable(ctx, id, ownerID)
	if err != nil {
		return domain.Content{}, err
	}
	return domain.Content{Hash: n.Revision, Cells: n.Cells}, nil
}

// CreateCell добавляет ячейку через операцию редактирования.
func (uc *NotebookUsecase) CreateCell(ctx context.Context, id, ownerID int64, hash string, kind domain.CellKind, index *int) (domain.Cell, error) {
	r, err := uc.AddCell(ctx, id, ownerID, hash, kind, "", index)
	return r.Cell, err
}

// DeleteCell удаляет выбранную ячейку через операцию редактирования.
func (uc *NotebookUsecase) DeleteCell(ctx context.Context, id, ownerID int64, hash string, index int) error {
	_, err := uc.repo.Apply(ctx, id, ownerID, hash, Operation{Kind: Delete, Index: &index})
	return err
}

// DeleteNotebook делегирует проверки владения и runtime одной транзакции хранилища.
func (uc *NotebookUsecase) DeleteNotebook(ctx context.Context, id, ownerID int64) error {
	return uc.repo.DeleteNotebook(ctx, id, ownerID)
}
