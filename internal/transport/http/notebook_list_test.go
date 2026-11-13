package httptransport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	notebookcursor "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/cursor"
	notebookdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery/http"
	notebookdomain "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	usersecurity "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	"go.uber.org/mock/gomock"
)

func TestNotebookListPipeline(t *testing.T) {
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"", 200}, {"?search=note&limit=1", 200}, {"?limit=100", 200},
		{"?limit=", 400}, {"?limit=0", 400}, {"?limit=-1", 400}, {"?limit=101", 400}, {"?limit=1.5", 400}, {"?limit=abc", 400}, {"?cursor=", 400},
	} {
		t.Run(tc.query, func(t *testing.T) {
			f := newRouterFixture(t)
			if tc.status == 200 {
				f.notebooks.EXPECT().List(gomock.Any(), int64(1), gomock.Any()).DoAndReturn(func(_ context.Context, _ int64, in usecase.ListInput) (usecase.ListPage, error) {
					if tc.query == "?search=note&limit=1" && (in.Search != "note" || in.Limit == nil || *in.Limit != 1) {
						t.Fatalf("params: %+v", in)
					}
					return usecase.ListPage{MatchedCount: 0, TotalCount: 7}, nil
				})
			}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/notebooks"+tc.query, nil)
			req.Header.Set("X-BFF-Key", "test-key")
			req.Header.Set("Authorization", "Bearer "+f.access)
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if tc.status == 400 {
				if string(got["code"]) != `"validation_error"` {
					t.Fatal(rec.Body)
				}
				return
			}
			if len(got) != 4 || string(got["items"]) != "[]" || string(got["next_cursor"]) != "null" || string(got["total_count"]) != "7" {
				t.Fatal(rec.Body)
			}
		})
	}
}

func TestNotebookListCursorPipeline(t *testing.T) {
	repo := NewMockRepository(gomock.NewController(t))
	stamp := time.Date(2026, 11, 13, 0, 0, 0, 123456000, time.UTC)
	codec := notebookcursor.New([]byte("pipeline-key"))
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, codec)
	tokens := usersecurity.NewAccessToken([]byte("jwt-test-secret"), time.Hour)
	access, err := tokens.Issue(testClaims(1))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewRouter(Server{NotebookHandler: notebookdelivery.NewHandler(uc)}, tokens, allowSessions{}, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	call := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/notebooks"+query, nil)
		req.Header.Set("X-BFF-Key", "test-key")
		req.Header.Set("Authorization", "Bearer "+access)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	repo.EXPECT().ListPage(gomock.Any(), int64(1), usecase.ListQuery{Limit: 1, Search: "note"}).Return(usecase.ListRows{
		Items: []notebookdomain.NotebookSummary{{ID: 2, Name: "note", UpdatedAt: stamp}, {ID: 1, Name: "note old", UpdatedAt: stamp}}, MatchedCount: 2, TotalCount: 3,
	}, nil)
	rec := call("?search=note&limit=1")
	var first api.NotebookListPage
	if err = json.Unmarshal(rec.Body.Bytes(), &first); err != nil || rec.Code != 200 || first.NextCursor == nil || len(first.Items) != 1 || first.Items[0].Id != 2 {
		t.Fatalf("first: %s %v", rec.Body, err)
	}
	repo.EXPECT().ListPage(gomock.Any(), int64(1), usecase.ListQuery{Limit: 24, Search: "note", After: &usecase.ListPosition{ID: 2, UpdatedAt: stamp}}).Return(usecase.ListRows{
		Items: []notebookdomain.NotebookSummary{{ID: 1, Name: "note old", UpdatedAt: stamp}}, MatchedCount: 2, TotalCount: 3,
	}, nil)
	rec = call("?search=note&cursor=" + url.QueryEscape(*first.NextCursor))
	var last api.NotebookListPage
	if err = json.Unmarshal(rec.Body.Bytes(), &last); err != nil || rec.Code != 200 || last.NextCursor != nil || len(last.Items) != 1 || last.Items[0].Id != 1 || last.MatchedCount != 2 || last.TotalCount != 3 {
		t.Fatalf("last: %s %v", rec.Body, err)
	}
	foreign, err := codec.Encode(2, "note", usecase.ListPosition{ID: 1, UpdatedAt: stamp})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"?cursor=broken", "?search=NOTE&cursor=" + url.QueryEscape(*first.NextCursor), "?search=note&cursor=" + url.QueryEscape(foreign)} {
		rec = call(query)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"code":"validation_error"`) {
			t.Fatalf("bad cursor: %d %s", rec.Code, rec.Body)
		}
	}
}
