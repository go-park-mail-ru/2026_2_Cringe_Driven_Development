package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"go.uber.org/mock/gomock"
)

func TestImportCreatesPrivateNotebook(t *testing.T) {
	ctx := t.Context()
	repo := NewMockRepository(gomock.NewController(t))
	doc := domain.Document{NBFormat: 4}
	cells := []domain.Cell{{ID: "original", Kind: domain.CellKindCode}}
	input := domain.Notebook{OwnerID: 7, Name: "My notebook", Document: doc, Cells: cells}
	saved := input
	saved.ID = 42
	repo.EXPECT().Create(ctx, input).Return(saved, nil)
	uc := NewNotebookUsecase(repo, NoActiveSessions{}, nil, WithImportDecoder(func(raw []byte) (domain.Document, []domain.Cell, int, error) {
		if string(raw) != "ipynb" {
			t.Fatal("unexpected input")
		}
		return doc, cells, 2, nil
	}))
	n, skipped, err := uc.Import(ctx, 7, " My notebook ", []byte("ipynb"))
	if err != nil || n.ID != 42 || !n.Access.IsOwner || skipped != 2 {
		t.Fatalf("notebook=%+v skipped=%d error=%v", n, skipped, err)
	}
}

func TestImportCanceledBeforeSave(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	repo := NewMockRepository(gomock.NewController(t))
	uc := NewNotebookUsecase(repo, NoActiveSessions{}, nil, WithImportDecoder(func([]byte) (domain.Document, []domain.Cell, int, error) {
		cancel()
		return domain.Document{}, nil, 0, nil
	}))
	if _, _, err := uc.Import(ctx, 7, "Name", []byte("ipynb")); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context, got %v", err)
	}
}
