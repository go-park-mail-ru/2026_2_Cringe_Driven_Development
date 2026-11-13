package httptransport

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	notebookcursor "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/cursor"
	notebookdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery/http"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	notebookusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	usersecurity "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	userdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/delivery/http"
	"go.uber.org/mock/gomock"
)

func TestS2SRejectionDoesNotTouchNotebookStorage(t *testing.T) {
	for _, op := range []struct {
		method, path string
		status       int
	}{
		{http.MethodPost, BaseURL + "/notebooks", http.StatusCreated},
		{http.MethodPost, BaseURL + "/notebooks/1/cells", http.StatusCreated},
		{http.MethodDelete, BaseURL + "/notebooks/1/cells/0", http.StatusNoContent},
	} {
		for _, invalid := range []string{"missing", "wrong", "previous", "duplicate header"} {
			t.Run(op.method+op.path+"/"+invalid, func(t *testing.T) {
				f := newRouterFixture(t)
				users, access := f.users, f.access
				ctrl := gomock.NewController(t)
				repo := NewMockRepository(ctrl)
				uc := notebookusecase.NewNotebookUsecase(repo, notebookusecase.NoActiveSessions{}, notebookcursor.New([]byte("list-test-key")))
				handler, err := NewRouter(Server{
					UserHandler:     userdelivery.NewHandler(users),
					NotebookHandler: notebookdelivery.NewHandler(uc),
				}, usersecurity.NewAccessToken([]byte("jwt-test-secret"), time.Hour), allowSessions{}, "test-key")
				if err != nil {
					t.Fatal(err)
				}
				req := routerRequest(op.method, op.path, "test-key", access)
				switch invalid {
				case "missing":
					req.Header.Del("X-BFF-Key")
				case "wrong", "previous":
					req.Header.Set("X-BFF-Key", invalid)
				case "duplicate header":
					req.Header.Add("X-BFF-Key", "test-key")
				}
				rec := serveRouter(handler, req)
				if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"s2s_forbidden"`) {
					t.Fatalf("rejection: %d %s", rec.Code, rec.Body)
				}
				// Во время отклонения ожиданий нет: любой вызов хранилища/сессий падает.
				// Положительный контроль разрешает точные вызовы только после отклонённого запроса.
				if op.path == BaseURL+"/notebooks" {
					repo.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, n domain.Notebook) (domain.Notebook, error) { n.ID = 1; return n, nil })
				} else {
					repo.EXPECT().Apply(gomock.Any(), int64(1), int64(1), "revision", gomock.Any()).Return(notebookusecase.Change{Hash: "next", Cell: domain.Cell{ID: "cell", Kind: domain.CellKindCode}}, nil)
				}
				positive := notebookMutationRequest(op.method, op.path, access)
				positive.Header.Set("If-Match", `"revision"`)
				rec = serveRouter(handler, positive)
				if rec.Code != op.status {
					t.Fatalf("valid request failed: status=%d body=%s", rec.Code, rec.Body)
				}
			})
		}
	}
}

func notebookMutationRequest(method, path, access string) *http.Request {
	req := routerRequest(method, path, "test-key", access)
	body := `{"name":"Notebook"}`
	if strings.HasSuffix(path, "/cells") {
		body = `{"kind":"code"}`
	}
	req.Body = io.NopCloser(strings.NewReader(body))
	req.ContentLength = int64(len(body))
	return req
}
