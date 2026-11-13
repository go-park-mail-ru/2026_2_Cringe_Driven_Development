package postgres

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	notebookcursor "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/cursor"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
)

func TestNotebookListSearch(t *testing.T) {
	repo, pool := testRepository(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,login,password_hash) OVERRIDING SYSTEM VALUE VALUES(2,'other','hash')`); err != nil {
		t.Fatal(err)
	}
	names := []string{"My Notebook", "NOTE", "Заметка", "100%", "under_score", `back\slash`, `a%_\b`, "two  spaces"}
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, notebookcursor.New([]byte("list-test-key")))
	for _, name := range names {
		if _, err := uc.Create(ctx, 1, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := uc.Create(ctx, 2, "NOTE"); err != nil {
		t.Fatal(err)
	}
	// Fix the timestamp explicitly: search assertions must not depend on wall-clock movement.
	if _, err := pool.Exec(ctx, `UPDATE notebooks SET updated_at=$1`, time.Date(2026, 11, 13, 0, 0, 0, 123456000, time.UTC)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		search string
		want   []int64
	}{
		{"", []int64{8, 7, 6, 5, 4, 3, 2, 1}}, {"note", []int64{2, 1}}, {"зАМ", []int64{3}},
		{"%", []int64{7, 4}}, {"_", []int64{7, 5}}, {`\`, []int64{7, 6}}, {`%_\`, []int64{7}},
		{"two spaces", []int64{}}, {"two  spaces", []int64{8}}, {"missing", []int64{}},
	} {
		t.Run(tc.search, func(t *testing.T) {
			got, err := repo.ListPage(ctx, 1, usecase.ListQuery{Search: tc.search, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			ids := []int64{}
			for _, n := range got.Items {
				ids = append(ids, n.ID)
			}
			if !reflect.DeepEqual(ids, tc.want) || got.TotalCount != 8 || got.MatchedCount != int64(len(tc.want)) {
				t.Fatalf("ids=%v counts=%d/%d want=%v", ids, got.MatchedCount, got.TotalCount, tc.want)
			}
		})
	}
}

func TestNotebookListPagination(t *testing.T) {
	repo, pool := testRepository(t)
	ctx := context.Background()
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, notebookcursor.New([]byte("list-test-key")))
	for range 26 {
		if _, err := uc.Create(ctx, 1, "note"); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Date(2026, 11, 13, 1, 0, 0, 123456000, time.UTC)
	if _, err := pool.Exec(ctx, `UPDATE notebooks SET updated_at=$1`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE notebooks SET updated_at=$1 WHERE id=1`, stamp.Add(time.Microsecond)); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{1, 2, 24, 26, 100} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			var after *usecase.ListPosition
			ids := []int64{}
			for range 30 {
				got, err := repo.ListPage(ctx, 1, usecase.ListQuery{Search: "note", Limit: limit, After: after})
				if err != nil {
					t.Fatal(err)
				}
				if got.MatchedCount != 26 || got.TotalCount != 26 {
					t.Fatal(got)
				}
				items := got.Items
				if len(items) > limit {
					items = items[:limit]
				}
				for _, n := range items {
					ids = append(ids, n.ID)
				}
				if len(got.Items) <= limit {
					break
				}
				last := items[len(items)-1]
				after = &usecase.ListPosition{UpdatedAt: last.UpdatedAt, ID: last.ID}
			}
			want := make([]int64, 1, 26)
			want[0] = 1
			for id := int64(26); id >= 2; id-- {
				want = append(want, id)
			}
			if !reflect.DeepEqual(ids, want) {
				t.Fatalf("%v != %v", ids, want)
			}
		})
	}
	if _, err := pool.Exec(ctx, `DELETE FROM notebooks WHERE id=26`); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListPage(ctx, 1, usecase.ListQuery{Limit: 1, After: &usecase.ListPosition{UpdatedAt: stamp, ID: 26}})
	if err != nil || len(got.Items) != 2 || got.Items[0].ID != 25 || got.TotalCount != 25 {
		t.Fatalf("deleted boundary: %+v %v", got, err)
	}
	got, err = repo.ListPage(ctx, 1, usecase.ListQuery{Limit: 24, After: &usecase.ListPosition{UpdatedAt: stamp, ID: 2}})
	if err != nil || len(got.Items) != 0 || got.MatchedCount != 25 {
		t.Fatalf("empty continuation: %+v %v", got, err)
	}
}
