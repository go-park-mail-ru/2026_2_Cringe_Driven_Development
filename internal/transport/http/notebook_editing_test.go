package httptransport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	notebookdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery/http"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	notebookusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	usersecurity "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	userdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/delivery/http"
)

type editingNotebooks struct {
	notebookusecase.Usecase
	calls int
	hash  string
}

func (n *editingNotebooks) Content(_ context.Context, _, owner int64) (domain.Content, error) {
	if owner != 1 {
		panic("owner context lost")
	}
	n.calls++
	return domain.Content{Hash: "new-hash", Cells: []domain.Cell{}}, nil
}
func (n *editingNotebooks) AddCell(ctx context.Context, id, owner int64, hash string, kind domain.CellKind, source string, _ *int) (domain.CellChange, error) {
	_, err := n.Content(ctx, id, owner)
	n.hash = hash
	return domain.CellChange{Hash: "new-hash", Cell: domain.Cell{ID: "cell", Kind: kind, Source: source}}, err
}
func (n *editingNotebooks) EditCell(ctx context.Context, id, owner int64, hash, cell, source string) (domain.CellChange, error) {
	_, err := n.Content(ctx, id, owner)
	n.hash = hash
	return domain.CellChange{Hash: "new-hash", Cell: domain.Cell{ID: cell, Kind: domain.CellKindCode, Source: source}}, err
}
func (n *editingNotebooks) MoveCell(ctx context.Context, id, owner int64, hash, _ string, _ int) (string, error) {
	_, err := n.Content(ctx, id, owner)
	n.hash = hash
	return "new-hash", err
}
func (n *editingNotebooks) RemoveCell(ctx context.Context, id, owner int64, hash, _ string) (string, error) {
	_, err := n.Content(ctx, id, owner)
	n.hash = hash
	return "new-hash", err
}

func TestNotebookEditingBFFPipeline(t *testing.T) {
	issuer := usersecurity.NewAccessToken([]byte("editing-test-key"), time.Hour)
	access, err := issuer.Issue(testClaims(1))
	if err != nil {
		t.Fatal(err)
	}
	notebooks := &editingNotebooks{}
	handler, err := NewRouter(Server{
		UserHandler:     userdelivery.NewHandler(nil),
		NotebookHandler: notebookdelivery.NewHandler(notebooks),
	}, issuer, allowSessions{}, "editing-bff-key")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, body  string
		want                int
		bearer, bff, cookie bool
	}{
		{"GET", "", "", 200, true, true, false},
		{"POST", "/cells", `{"kind":"markdown","source":"# text"}`, 201, true, true, false},
		{"PATCH", "/cells/cell", `{"source":"print(2)"}`, 200, true, true, false},
		{"POST", "/cells/cell/move", `{"index":0}`, 200, true, true, false},
		{"DELETE", "/cells/cell", "", 200, true, true, false},
		{"GET", "", ``, 401, false, true, false},
		{"GET", "", ``, 401, false, true, true},
		{"GET", "", ``, 403, true, false, false},
		{"POST", "/cells", `{"kind":"markdown","source":"# text"}`, 401, false, true, false},
		{"POST", "/cells", `{"kind":"markdown","source":"# text"}`, 401, false, true, true},
		{"POST", "/cells", `{"kind":"markdown","source":"# text"}`, 403, true, false, false},
		{"PATCH", "/cells/cell", `{"source":"print(2)"}`, 401, false, true, false},
		{"PATCH", "/cells/cell", `{"source":"print(2)"}`, 401, false, true, true},
		{"PATCH", "/cells/cell", `{"source":"print(2)"}`, 403, true, false, false},
		{"POST", "/cells/cell/move", `{"index":0}`, 401, false, true, false},
		{"POST", "/cells/cell/move", `{"index":0}`, 401, false, true, true},
		{"POST", "/cells/cell/move", `{"index":0}`, 403, true, false, false},
		{"DELETE", "/cells/cell", ``, 401, false, true, false},
		{"DELETE", "/cells/cell", ``, 401, false, true, true},
		{"DELETE", "/cells/cell", ``, 403, true, false, false},
	} {
		t.Run(tc.method+tc.path+http.StatusText(tc.want), func(t *testing.T) {
			before := notebooks.calls
			req := httptest.NewRequest(tc.method, BaseURL+"/notebooks/1/content"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Origin", "https://cellestial.ru")
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			if tc.cookie {
				req.AddCookie(&http.Cookie{Name: "access_token", Value: access})
			}
			if tc.bearer {
				req.Header.Set("Authorization", "Bearer "+access)
			}
			if tc.bff {
				req.Header.Set("X-BFF-Key", "editing-bff-key")
			}
			if tc.method != "GET" {
				req.Header.Set("If-Match", `"old-hash"`)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body)
			}
			if rec.Header().Get("X-Request-ID") == "" {
				t.Fatal("missing request ID")
			}
			if tc.want < 300 {
				if notebooks.calls != before+1 || rec.Header().Get("ETag") != `"new-hash"` {
					t.Fatalf("calls=%d ETag=%s", notebooks.calls, rec.Header().Get("ETag"))
				}
				if tc.method != "GET" && notebooks.hash != "old-hash" {
					t.Fatal("hash not forwarded")
				}
			} else if notebooks.calls != before {
				t.Fatal("rejected request reached notebook usecase")
			}
		})
	}
}
