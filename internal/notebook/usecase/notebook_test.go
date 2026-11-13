package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"go.uber.org/mock/gomock"
)

func TestListUsesOneRuntimeLookup(t *testing.T) {
	ctx := context.Background()
	for _, active := range []int64{0, 1, 2, 99, 0} {
		ctrl := gomock.NewController(t)
		repo := NewMockRepository(ctrl)
		sessions := NewMockActiveSessions(ctrl)
		repo.EXPECT().ListPage(ctx, int64(7), ListQuery{Limit: 24}).Return(ListRows{Items: []domain.NotebookSummary{{ID: 1, PreviewCode: "one"}, {ID: 2, PreviewCode: "two"}}}, nil)
		sessions.EXPECT().ActiveNotebook(ctx, int64(7)).Return(active, nil).Times(1)
		got, err := NewNotebookUsecase(repo, sessions, nil).List(ctx, 7, ListInput{})
		if err != nil {
			t.Fatal(err)
		}
		if got.Items[0].HasActiveSession != (active == 1) || got.Items[1].HasActiveSession != (active == 2) || got.Items[0].PreviewCode != "one" {
			t.Fatalf("list: %+v", got)
		}
	}
}
func TestRuntimeFailureIsNotHidden(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := NewMockRepository(ctrl)
	sessions := NewMockActiveSessions(ctrl)
	repo.EXPECT().ListPage(gomock.Any(), int64(1), ListQuery{Limit: 24}).Return(ListRows{}, nil)
	failure := errors.New("unavailable")
	sessions.EXPECT().ActiveNotebook(gomock.Any(), int64(1)).Return(int64(0), failure)
	if _, err := NewNotebookUsecase(repo, sessions, nil).List(context.Background(), 1, ListInput{}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
func TestEditingTransmitsOnlyOperation(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := NewMockRepository(ctrl)
	uc := NewNotebookUsecase(repo, NoActiveSessions{}, nil)
	repo.EXPECT().Apply(gomock.Any(), int64(3), int64(7), "revision", Operation{Kind: Edit, CellID: "cell", Source: "new"}).Return(Change{Hash: "next", Cell: domain.Cell{ID: "cell", Source: "new"}}, nil)
	got, err := uc.EditCell(context.Background(), 3, 7, "revision", "cell", "new")
	if err != nil || got.Hash != "next" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, source := range []string{strings.Repeat("a", 65537), strings.Repeat("я", 32769), "\x00", string([]byte{255})} {
		if _, err = uc.EditCell(context.Background(), 3, 7, "revision", "cell", source); err == nil {
			t.Fatal("accepted invalid source")
		}
	}
}
func TestCreateAtomicInitialCell(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := NewMockRepository(ctrl)
	uc := NewNotebookUsecase(repo, NoActiveSessions{}, nil)
	repo.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, n domain.Notebook) (domain.Notebook, error) {
		if n.Name != "name" || n.OwnerID != 7 || len(n.Cells) != 1 || n.Cells[0].Kind != domain.CellKindCode || n.Cells[0].ID == "" || string(n.Cells[0].Outputs) != "[]" || !n.Cells[0].HasExecutionCount {
			t.Fatalf("create: %+v", n)
		}
		n.ID = 3
		return n, nil
	})
	if _, err := uc.Create(context.Background(), 7, " name "); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.Create(context.Background(), 7, " \t "); !errors.Is(err, ErrEmptyName) {
		t.Fatal(err)
	}
}
