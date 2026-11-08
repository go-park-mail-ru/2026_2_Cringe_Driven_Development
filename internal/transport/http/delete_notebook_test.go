package httptransport

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	notebookusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"go.uber.org/mock/gomock"
)

func TestDeleteNotebookHTTP(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "deleted", status: http.StatusNoContent},
		{name: "not found", err: notebookusecase.ErrNotebookNotFound, status: http.StatusNotFound, code: "not_found"},
		{name: "busy", err: notebookusecase.ErrNotebookInUse, status: http.StatusConflict, code: "notebook_in_use"},
		{name: "storage error", err: errors.New("private database error"), status: http.StatusInternalServerError, code: "internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRouterFixture(t)
			f.notebooks.EXPECT().DeleteNotebook(gomock.Any(), int64(42), int64(1)).Return(tc.err)
			req := httptest.NewRequest(http.MethodDelete, BaseURL+"/notebooks/42", nil)
			req.Header.Set("X-BFF-Key", "test-key")
			req.Header.Set("Authorization", "Bearer "+f.access)
			rec := serveRouter(f.handler, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
			if tc.status == http.StatusNoContent && rec.Body.Len() != 0 {
				t.Fatalf("204 body=%s", rec.Body)
			}
			if tc.code != "" && !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("body=%s", rec.Body)
			}
			if tc.name == "busy" && !strings.Contains(rec.Body.String(), "Сначала остановите runtime-сессию") {
				t.Fatalf("missing guidance: %s", rec.Body)
			}
			if strings.Contains(rec.Body.String(), "private database error") {
				t.Fatal("storage error exposed")
			}
		})
	}
}

func TestDeleteNotebookHTTPRejectsInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		name, path, key        string
		noBearer, duplicateKey bool
		status                 int
		code                   string
	}{
		{name: "missing key", path: "1", status: 403, code: "s2s_forbidden"},
		{name: "wrong key", path: "1", key: "wrong", status: 403, code: "s2s_forbidden"},
		{name: "duplicate key", path: "1", key: "test-key", duplicateKey: true, status: 403, code: "s2s_forbidden"},
		{name: "no bearer", path: "1", key: "test-key", noBearer: true, status: 401, code: "unauthorized"},
		{name: "zero", path: "0", key: "test-key", status: 400, code: "validation_error"},
		{name: "negative", path: "-1", key: "test-key", status: 400, code: "validation_error"},
		{name: "not integer", path: "abc", key: "test-key", status: 400, code: "validation_error"},
		{name: "overflow", path: "9223372036854775808", key: "test-key", status: 400, code: "validation_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRouterFixture(t)
			access := f.access
			if tc.noBearer {
				access = ""
			}
			req := httptest.NewRequest(http.MethodDelete, BaseURL+"/notebooks/"+tc.path, nil)
			if tc.key != "" {
				req.Header.Set("X-BFF-Key", tc.key)
			}
			if access != "" {
				req.Header.Set("Authorization", "Bearer "+access)
			}
			if tc.duplicateKey {
				req.Header.Add("X-BFF-Key", tc.key)
			}
			rec := serveRouter(f.handler, req)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
}
