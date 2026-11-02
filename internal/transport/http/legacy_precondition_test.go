package httptransport

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	notebookdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery/http"
	usersecurity "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	userdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/delivery/http"
)

func TestLegacyMutationsRequireOneStrongETag(t *testing.T) {
	tokens := usersecurity.NewAccessToken([]byte("legacy-test"), time.Hour)
	access, err := tokens.Issue(testClaims(1))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewRouter(Server{UserHandler: userdelivery.NewHandler(nil), NotebookHandler: notebookdelivery.NewHandler(bffNotebooks{})}, tokens, allowSessions{}, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		for _, headers := range [][]string{nil, {`*`}, {`W/"hash"`}, {`"one", "two"`}, {`"one"`, `"two"`}} {
			path := BaseURL + "/notebooks/1/cells"
			if method == http.MethodDelete {
				path += "/0"
			}
			req := routerRequest(method, path, "test-key", access)
			if method == http.MethodPost {
				req.Body = io.NopCloser(strings.NewReader(`{"kind":"code"}`))
			}
			for _, h := range headers {
				req.Header.Add("If-Match", h)
			}
			rec := serveRouter(handler, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s headers=%v: %d %s", method, headers, rec.Code, rec.Body)
			}
		}
	}
}
