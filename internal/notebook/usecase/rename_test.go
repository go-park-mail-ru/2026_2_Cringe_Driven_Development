package usecase

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"go.uber.org/mock/gomock"
)

type renameContextKey struct{}

func TestRenameNormalizesAndDelegates(t *testing.T) {
	testRenameText(t, " \tНовое название\n ", "Новое название")
}

func TestRenamePreservesNameText(t *testing.T) {
	testRenameText(t, "\u00a0  <b>Я 👩‍💻</b>  текст  \u2003", "<b>Я 👩‍💻</b>  текст")
}

func testRenameText(t *testing.T, input, name string) {
	t.Helper()
	ctrl := gomock.NewController(t)
	repo, sessions := NewMockRepository(ctrl), NewMockActiveSessions(ctrl)
	ctx := context.WithValue(t.Context(), renameContextKey{}, "request")
	want := domain.Notebook{ID: 42, OwnerID: 7, Name: name, Revision: "unchanged", Cells: []domain.Cell{}}
	repo.EXPECT().Rename(ctx, int64(42), int64(7), name).Return(want, nil)
	got, err := NewNotebookUsecase(repo, sessions).Rename(ctx, 42, 7, input)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("rename: %+v %v", got, err)
	}
}

func TestRenameRejectsEmptyName(t *testing.T) {
	for _, name := range []string{"", " \t\n ", "\u00a0\u2003"} {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			uc := NewNotebookUsecase(NewMockRepository(ctrl), NewMockActiveSessions(ctrl))
			got, err := uc.Rename(t.Context(), 42, 7, name)
			if !errors.Is(err, ErrEmptyName) || !reflect.DeepEqual(got, domain.Notebook{}) {
				t.Fatalf("invalid name: %+v %v", got, err)
			}
		})
	}
}

func TestRenamePropagatesRepositoryErrors(t *testing.T) {
	for _, failure := range []error{ErrNotebookNotFound, errors.New("database unavailable")} {
		t.Run(failure.Error(), func(t *testing.T) {
			ctrl := gomock.NewController(t)
			repo := NewMockRepository(ctrl)
			repo.EXPECT().Rename(gomock.Any(), int64(42), int64(7), "name").Return(domain.Notebook{}, failure)
			_, err := NewNotebookUsecase(repo, NewMockActiveSessions(ctrl)).Rename(t.Context(), 42, 7, "name")
			if !errors.Is(err, failure) {
				t.Fatal(err)
			}
		})
	}
}
