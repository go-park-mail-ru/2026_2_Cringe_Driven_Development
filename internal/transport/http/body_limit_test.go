package httptransport

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/ipynb"
	notebookdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery/http"
	notebookusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	usersecurity "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	userdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/delivery/http"
	"go.uber.org/mock/gomock"
)

func TestOversizedRequestIsRejectedBeforeUsecase(t *testing.T) {
	for _, length := range []int64{-1, 600000} {
		f := newRouterFixture(t)
		req := routerRequest(http.MethodPost, BaseURL+"/notebooks", "test-key", f.access)
		req.Body = io.NopCloser(strings.NewReader(`{"name":"` + strings.Repeat("x", 600000) + `"}`))
		req.ContentLength = length
		rec := serveRouter(f.handler, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("length=%d: status=%d body=%s", length, rec.Code, rec.Body)
		}
	}
}

func TestOversizedSourceReturns413WithoutStorage(t *testing.T) {
	for _, source := range []string{strings.Repeat("x", 65537), strings.Repeat("я", 32769)} {
		for _, method := range []string{http.MethodPost, http.MethodPatch} {
			ctrl := gomock.NewController(t)
			uc := notebookusecase.NewNotebookUsecase(NewMockMetadata(ctrl), NewMockFiles(ctrl), ipynb.Codec{})
			tokens := usersecurity.NewAccessToken([]byte("source-test"), time.Hour)
			access, err := tokens.Issue(testClaims(1))
			if err != nil {
				t.Fatal(err)
			}
			handler, err := NewRouter(Server{UserHandler: userdelivery.NewHandler(nil), NotebookHandler: notebookdelivery.NewHandler(uc)}, tokens, allowSessions{}, "test-key")
			if err != nil {
				t.Fatal(err)
			}
			path := BaseURL + "/notebooks/1/content/cells"
			body := `{"kind":"code","source":` + strconv.Quote(source) + `}`
			if method == http.MethodPatch {
				path += "/cell"
				body = `{"source":` + strconv.Quote(source) + `}`
			}
			req := routerRequest(method, path, "test-key", access)
			req.Header.Set("If-Match", `"hash"`)
			req.Body = io.NopCloser(strings.NewReader(body))
			req.ContentLength = int64(len(body))
			rec := serveRouter(handler, req)
			if rec.Code != 413 {
				t.Fatalf("%s source bytes=%d status=%d body=%s", method, len(source), rec.Code, rec.Body)
			}
		}
	}
}

func TestNotebookBodyLimitDoesNotChangeAuthRoutes(t *testing.T) {
	for _, path := range []string{"/auth/register", "/auth/login", "/auth/refresh", "/auth/logout"} {
		f := newRouterFixture(t)
		req := routerRequest(http.MethodPost, BaseURL+path, "test-key", f.access)
		body := "{" + strings.Repeat(" ", 600000)
		req.Body = io.NopCloser(strings.NewReader(body))
		req.ContentLength = int64(len(body))
		rec := serveRouter(f.handler, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d want original JSON validation 400", path, rec.Code)
		}
	}
}
