package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNotebookCachePolicy(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		noStore      bool
	}{
		{"GET", "/api/v1/notebooks/1", true},
		{"GET", "/api/v1/notebooks/1/content", true},
		{"GET", "/api/v1/notebooks/1/sharing", true},
		{"PUT", "/api/v1/notebooks/1/sharing", true},
		{"GET", "/api/v1/notebooks", false},
		{"GET", "/api/v1/runtime/sessions", false},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			NotebookCachePolicy("/api/v1")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })).ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if got := rec.Header().Get("Cache-Control"); (got == "no-store") != tc.noStore {
				t.Fatalf("Cache-Control=%q want no-store=%v", got, tc.noStore)
			}
		})
	}
}
