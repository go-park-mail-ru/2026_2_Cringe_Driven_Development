package httptransport

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/ipynb"
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
				meta := NewMockMetadata(ctrl)
				data, err := ipynb.Build([]domain.Cell{{ID: "cell", Kind: domain.CellKindCode}})
				if err != nil {
					t.Fatal(err)
				}
				files := NewMockFiles(ctrl)
				uc := notebookusecase.NewNotebookUsecase(meta, files, ipynb.Codec{})
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
				// No expectations existed during rejection: any storage/session call fails.
				// Positive control grants exact calls only after the rejected request.
				if op.path == BaseURL+"/notebooks" {
					var key string
					put := files.EXPECT().Put(gomock.Any(), gomock.Cond(func(k string) bool { return strings.HasPrefix(k, "notebooks/") && strings.HasSuffix(k, ".ipynb") }), gomock.Cond(func(b []byte) bool {
						cells, err := ipynb.Parse(b)
						return err == nil && len(cells) == 1 && cells[0].ID != "" && cells[0].Kind == domain.CellKindCode && cells[0].Source == ""
					})).DoAndReturn(func(_ context.Context, k string, _ []byte) error { key = k; return nil })
					meta.EXPECT().Create(gomock.Any(), gomock.Cond(func(n domain.Notebook) bool {
						return n.OwnerID == 1 && n.Name == "Notebook" && n.FileKey == key && n.CellsCount == 1
					})).After(put).DoAndReturn(func(_ context.Context, n domain.Notebook) (domain.Notebook, error) { n.ID = 1; return n, nil })
				} else {
					wantCount := 2
					if op.method == http.MethodDelete {
						wantCount = 0
					}
					meta.EXPECT().Modify(gomock.Any(), int64(1), int64(1), gomock.Any()).DoAndReturn(func(_ context.Context, _ int64, _ int64, fn func(domain.Notebook) (notebookusecase.MetadataUpdate, error)) error {
						update, err := fn(domain.Notebook{ID: 1, OwnerID: 1, FileKey: "fixture.ipynb", CellsCount: 1})
						if err != nil || (update.CellsCount != wantCount || !update.Changed || update.FileKey == "fixture.ipynb") {
							t.Fatalf("callback count=%d err=%v", update.CellsCount, err)
						}
						return err
					})
					gomock.InOrder(files.EXPECT().Get(gomock.Any(), "fixture.ipynb").Return(data, nil), files.EXPECT().Put(gomock.Any(), gomock.Cond(func(k string) bool { return strings.HasPrefix(k, "notebooks/") && strings.HasSuffix(k, ".ipynb") }), gomock.Cond(func(b []byte) bool {
						cells, err := ipynb.Parse(b)
						if err != nil || len(cells) != wantCount {
							return false
						}
						if wantCount == 2 {
							return cells[0].ID == "cell" && cells[1].Kind == domain.CellKindCode
						}
						return true
					})).Return(nil))
				}
				positive := notebookMutationRequest(op.method, op.path, access)
				positive.Header.Set("If-Match", fmt.Sprintf("\"%x\"", sha256.Sum256(data)))
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
