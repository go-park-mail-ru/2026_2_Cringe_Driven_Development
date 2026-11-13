package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/cursor"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
)

type listRepo struct {
	usecase.Repository
	query usecase.ListQuery
	calls int
	rows  usecase.ListRows
	err   error
}

func (r *listRepo) ListPage(_ context.Context, owner int64, q usecase.ListQuery) (usecase.ListRows, error) {
	if owner != 7 {
		panic("wrong owner")
	}
	r.calls++
	r.query = q
	return r.rows, r.err
}

type activeSession struct{ err error }

func (s activeSession) ActiveNotebook(context.Context, int64) (int64, error) { return 2, s.err }
func TestListPage(t *testing.T) {
	ctx := context.Background()
	stamp := time.Date(2026, 11, 13, 1, 0, 0, 123456000, time.UTC)
	repo := &listRepo{rows: usecase.ListRows{Items: []domain.NotebookSummary{{ID: 3, UpdatedAt: stamp}, {ID: 2, UpdatedAt: stamp}, {ID: 1, UpdatedAt: stamp}}, MatchedCount: 8, TotalCount: 10}}
	uc := usecase.NewNotebookUsecase(repo, activeSession{}, cursor.New([]byte("key")))
	limit := 2
	page, err := uc.List(ctx, 7, usecase.ListInput{Search: " new ", Limit: &limit})
	if err != nil || len(page.Items) != 2 || page.NextCursor == nil || page.MatchedCount != 8 || page.TotalCount != 10 || !page.Items[1].HasActiveSession || repo.query.Search != "new" || repo.query.Limit != 2 {
		t.Fatalf("%+v %v %+v", page, err, repo.query)
	}
	limit = 1
	_, err = uc.List(ctx, 7, usecase.ListInput{Search: "new", Limit: &limit, Cursor: page.NextCursor})
	if err != nil || repo.query.After.ID != 2 || !repo.query.After.UpdatedAt.Equal(stamp) {
		t.Fatal(repo.query, err)
	}
	before := repo.calls
	for _, in := range []usecase.ListInput{{Limit: new(int)}, {Limit: ptr(101)}, {Cursor: ptr("")}, {Cursor: ptr("bad")}, {Search: "NEW", Cursor: page.NextCursor}} {
		if _, err = uc.List(ctx, 7, in); !errors.Is(err, usecase.ErrInvalidList) {
			t.Fatal("accepted", in, err)
		}
	}
	if repo.calls != before {
		t.Fatal("invalid input reached database")
	}
	repo.rows = usecase.ListRows{}
	page, err = uc.List(ctx, 7, usecase.ListInput{Search: " \t "})
	if err != nil || page.Items == nil || len(page.Items) != 0 || page.NextCursor != nil || repo.query.Search != "" || repo.query.Limit != 24 {
		t.Fatalf("empty: %+v %v", page, err)
	}
	failure := errors.New("offline")
	repo.err = failure
	if _, err = uc.List(ctx, 7, usecase.ListInput{}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	repo.err = nil
	uc = usecase.NewNotebookUsecase(repo, activeSession{err: failure}, cursor.New([]byte("key")))
	if _, err = uc.List(ctx, 7, usecase.ListInput{}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
func ptr[T any](v T) *T { return &v }
