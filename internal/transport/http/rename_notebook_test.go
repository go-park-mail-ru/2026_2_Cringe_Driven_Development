package httptransport

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	notebookdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery/http"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	notebookusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	usersecurity "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	userdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/delivery/http"
	"go.uber.org/mock/gomock"
)

type renameHTTPFixture struct {
	handler http.Handler
	repo    *MockRepository
	access  string
}

func newRenameHTTPFixture(t *testing.T) renameHTTPFixture {
	t.Helper()
	repo := NewMockRepository(gomock.NewController(t))
	tokens := usersecurity.NewAccessToken([]byte("rename-test-secret"), time.Hour)
	access, err := tokens.Issue(testClaims(1))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewRouter(Server{UserHandler: userdelivery.NewHandler(nil), NotebookHandler: notebookdelivery.NewHandler(notebookusecase.NewNotebookUsecase(repo, notebookusecase.NoActiveSessions{}))}, tokens, allowSessions{}, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	return renameHTTPFixture{handler: handler, repo: repo, access: access}
}

func (f renameHTTPFixture) request(path, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPatch, BaseURL+"/notebooks/"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-BFF-Key", "test-key")
	r.Header.Set("Authorization", "Bearer "+f.access)
	return r
}

func TestRenameNotebookHTTP(t *testing.T) {
	f := newRenameHTTPFixture(t)
	stamp := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	n := domain.Notebook{ID: 42, OwnerID: 1, Name: "Новое название", CreatedAt: stamp, UpdatedAt: stamp.Add(time.Minute), Cells: []domain.Cell{{ID: "cell", Kind: domain.CellKindCode, Source: "print(42)"}}}
	f.repo.EXPECT().Rename(gomock.Any(), int64(42), int64(1), "Новое название").Return(n, nil)
	r := serveRouter(f.handler, f.request("42", `{"name":"  Новое название  "}`))
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	var got api.Notebook
	if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := api.Notebook{Id: 42, Name: "Новое название", CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt, Cells: []api.Cell{{Id: "cell", Kind: api.CellKindCode, Source: "print(42)"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("full notebook: %+v", got)
	}
}

func TestRenameNotebookHTTPEmptyCells(t *testing.T) {
	f := newRenameHTTPFixture(t)
	f.repo.EXPECT().Rename(gomock.Any(), int64(42), int64(1), "empty").Return(domain.Notebook{ID: 42, Name: "empty"}, nil)
	r := serveRouter(f.handler, f.request("42", `{"name":"empty"}`))
	var got struct {
		Cells json.RawMessage `json:"cells"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if r.Code != 200 || string(got.Cells) != "[]" {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
}

func TestRenameNotebookHTTPValidation(t *testing.T) {
	for _, body := range []string{"", `null`, `{}`, `{"name":null}`, `42`, `{"name":42}`, `{"name":false}`, `{"name":[]}`, `{"name":""}`, `{"name":" \t\n "}`, `{"name":"x"`} {
		t.Run(body, func(t *testing.T) {
			f := newRenameHTTPFixture(t)
			r := serveRouter(f.handler, f.request("42", body))
			if r.Code != 400 || !strings.Contains(r.Body.String(), `"code":"validation_error"`) {
				t.Fatalf("%d %s", r.Code, r.Body)
			}
		})
	}
	for _, id := range []string{"0", "-1", "abc", "9223372036854775808"} {
		t.Run(id, func(t *testing.T) {
			f := newRenameHTTPFixture(t)
			r := serveRouter(f.handler, f.request(id, `{"name":"valid"}`))
			if r.Code != 400 {
				t.Fatalf("%d %s", r.Code, r.Body)
			}
		})
	}
}

func TestRenameNotebookHTTPNameLimitsMatchCreate(t *testing.T) {
	for _, method := range []string{http.MethodPatch, http.MethodPost} {
		for _, tc := range []struct {
			name string
			code int
		}{{strings.Repeat("я", 128), 200}, {strings.Repeat("я", 129), 400}, {" " + strings.Repeat("я", 127) + " ", 400}} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				f := newRenameHTTPFixture(t)
				body, _ := json.Marshal(map[string]string{"name": tc.name})
				r := f.request("42", string(body))
				want := tc.code
				if method == http.MethodPost {
					r.Method = method
					r.URL.Path = BaseURL + "/notebooks"
					if tc.code == 200 {
						want = 201
						f.repo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.Notebook{ID: 42, Name: tc.name}, nil)
					}
				} else if tc.code == 200 {
					f.repo.EXPECT().Rename(gomock.Any(), int64(42), int64(1), tc.name).Return(domain.Notebook{ID: 42, Name: tc.name}, nil)
				}
				rec := serveRouter(f.handler, r)
				if rec.Code != want {
					t.Fatalf("%d %s", rec.Code, rec.Body)
				}
			})
		}
	}
}

func TestRenameNotebookHTTPErrorResponses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		code      int
		errorCode string
	}{
		{"not found", notebookusecase.ErrNotebookNotFound, 404, "not_found"},
		{"storage", errors.New("private database error"), 500, "internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRenameHTTPFixture(t)
			f.repo.EXPECT().Rename(gomock.Any(), int64(42), int64(1), "name").Return(domain.Notebook{}, tc.err)
			r := serveRouter(f.handler, f.request("42", `{"name":"name"}`))
			var got api.Error
			if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if r.Code != tc.code || string(got.Code) != tc.errorCode || strings.Contains(r.Body.String(), "private database error") {
				t.Fatalf("%d %s", r.Code, r.Body)
			}
		})
	}
}

func TestRenameNotebookHTTPAuthPrecedesValidation(t *testing.T) {
	for _, tc := range []struct {
		name, key, token string
		duplicate        bool
		code             int
	}{
		{"missing key", "", "", false, 403}, {"wrong key", "wrong", "invalid", false, 403}, {"duplicate key", "test-key", "", true, 403}, {"missing bearer", "test-key", "", false, 401}, {"invalid bearer", "test-key", "invalid", false, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRenameHTTPFixture(t)
			r := f.request("42", `{"name":`)
			r.Header.Del("X-BFF-Key")
			r.Header.Del("Authorization")
			if tc.key != "" {
				r.Header.Set("X-BFF-Key", tc.key)
			}
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.duplicate {
				r.Header.Add("X-BFF-Key", tc.key)
			}
			rec := serveRouter(f.handler, r)
			code := "unauthorized"
			if tc.code == 403 {
				code = "s2s_forbidden"
			}
			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), `"code":"`+code+`"`) {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestRenameNotebookHTTPIgnoresContentPrecondition(t *testing.T) {
	for _, hash := range []string{"", `"outdated"`, `W/"old"`} {
		t.Run(hash, func(t *testing.T) {
			f := newRenameHTTPFixture(t)
			f.repo.EXPECT().Rename(gomock.Any(), int64(42), int64(1), "name").Return(domain.Notebook{ID: 42, Name: "name"}, nil)
			r := f.request("42", `{"name":"name"}`)
			if hash != "" {
				r.Header.Set("If-Match", hash)
			}
			rec := serveRouter(f.handler, r)
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
}
