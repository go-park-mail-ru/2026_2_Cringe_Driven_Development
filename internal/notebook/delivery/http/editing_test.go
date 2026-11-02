package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
)

type editingFake struct {
	usecase.Usecase
	err   error
	calls int
	hash  string
	owner int64
}

func (f *editingFake) Content(_ context.Context, _, owner int64) (domain.Content, error) {
	f.calls++
	f.owner = owner
	return domain.Content{Hash: "new-hash", Cells: []domain.Cell{}}, f.err
}
func (f *editingFake) AddCell(_ context.Context, _, owner int64, hash string, kind domain.CellKind, source string, _ *int) (domain.CellChange, error) {
	f.calls++
	f.hash = hash
	f.owner = owner
	return domain.CellChange{Hash: "new-hash", Cell: domain.Cell{ID: "a", Kind: kind, Source: source}}, f.err
}
func (f *editingFake) EditCell(_ context.Context, _, owner int64, hash, id, source string) (domain.CellChange, error) {
	f.calls++
	f.hash = hash
	f.owner = owner
	return domain.CellChange{Hash: "new-hash", Cell: domain.Cell{ID: id, Kind: domain.CellKindCode, Source: source}}, f.err
}
func (f *editingFake) MoveCell(_ context.Context, _, owner int64, hash, _ string, _ int) (string, error) {
	f.calls++
	f.hash = hash
	f.owner = owner
	return "new-hash", f.err
}
func (f *editingFake) RemoveCell(_ context.Context, _, owner int64, hash, _ string) (string, error) {
	f.calls++
	f.hash = hash
	f.owner = owner
	return "new-hash", f.err
}

func editingRequest(t *testing.T, f *editingFake, method, path, body string, headers []string, authorized bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/notebooks/1/content"+path, strings.NewReader(body))
	req.Header.Set("X-BFF-Key", "test-key")
	if authorized {
		req.Header.Set("Authorization", "Bearer access-1")
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, h := range headers {
		req.Header.Add("If-Match", h)
	}
	rec := httptest.NewRecorder()
	newRouter(t, f).ServeHTTP(rec, req)
	return rec
}

func TestEditingHTTPResponsesAndHashForwarding(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "", "", 200},
		{"POST", "/cells", `{"kind":"markdown","source":"# text"}`, 201},
		{"PATCH", "/cells/a", `{"source":"print(2)"}`, 200},
		{"POST", "/cells/a/move", `{"index":0}`, 200},
		{"DELETE", "/cells/a", "", 200},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			f := &editingFake{}
			rec := editingRequest(t, f, tc.method, tc.path, tc.body, []string{`"old-hash"`}, true)
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
			if rec.Header().Get("ETag") != `"new-hash"` {
				t.Fatalf("ETag=%s", rec.Header().Get("ETag"))
			}
			var body struct {
				Hash string `json:"hash"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Hash != "new-hash" || f.owner != 1 || f.calls != 1 {
				t.Fatalf("body=%s owner=%d calls=%d", rec.Body, f.owner, f.calls)
			}
			if tc.method != "GET" && f.hash != "old-hash" {
				t.Fatalf("If-Match forwarded as %q", f.hash)
			}
		})
	}
}

func TestEditingHTTPRejectsInvalidPreconditions(t *testing.T) {
	for _, headers := range [][]string{nil, {""}, {"*"}, {`W/"hash"`}, {`"a", "b"`}, {`""`}, {`"a"`, `"b"`}, {`hash`}} {
		f := &editingFake{}
		rec := editingRequest(t, f, "PATCH", "/cells/a", `{"source":"x"}`, headers, true)
		if rec.Code != 400 || f.calls != 0 {
			t.Fatalf("headers=%v status=%d calls=%d body=%s", headers, rec.Code, f.calls, rec.Body)
		}
	}
}

func TestEditingHTTPMapsFailures(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{usecase.ErrNotebookConflict, 409, "notebook_conflict"},
		{usecase.ErrCellNotFound, 404, "not_found"},
		{usecase.ErrNotebookNotFound, 404, "not_found"},
		{usecase.ErrIndexOutOfRange, 400, "validation_error"},
		{errors.New("storage-secret"), 500, "internal"},
	} {
		f := &editingFake{err: tc.err}
		rec := editingRequest(t, f, "PATCH", "/cells/a", `{"source":"x"}`, []string{`"hash"`}, true)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) || strings.Contains(rec.Body.String(), "storage-secret") {
			t.Fatalf("err=%v status=%d body=%s", tc.err, rec.Code, rec.Body)
		}
	}
	f := &editingFake{err: usecase.ErrTooManyCells}
	rec := editingRequest(t, f, "POST", "/cells", `{"kind":"code","source":""}`, []string{`"hash"`}, true)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"too_many_cells"`) {
		t.Fatalf("limit: %d %s", rec.Code, rec.Body)
	}
	f = &editingFake{}
	rec = editingRequest(t, f, "GET", "", "", nil, false)
	if rec.Code != http.StatusUnauthorized || f.calls != 0 {
		t.Fatalf("unauthenticated: %d calls=%d", rec.Code, f.calls)
	}
}
