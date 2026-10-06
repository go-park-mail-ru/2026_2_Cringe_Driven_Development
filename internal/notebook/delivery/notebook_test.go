package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/httperr"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/middleware"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/models"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	userdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/delivery"
	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/mux"
)

// fakeUsecase отдаёт заранее заданный результат.
type fakeUsecase struct {
	list     []models.NotebookSummary
	notebook models.Notebook
	cell     models.Cell
	err      error
	// gotIndex запоминает, какой index хендлер передал в CreateCell.
	gotIndex *int
}

var _ usecase.Usecase = (*fakeUsecase)(nil)

func (f *fakeUsecase) List(context.Context, int64) ([]models.NotebookSummary, error) {
	return f.list, f.err
}

func (f *fakeUsecase) Create(context.Context, int64, string) (models.Notebook, error) {
	return f.notebook, f.err
}

func (f *fakeUsecase) Get(context.Context, int64, int64) (models.Notebook, error) {
	return f.notebook, f.err
}

func (f *fakeUsecase) CreateCell(_ context.Context, _, _ int64, _ models.CellKind, index *int) (models.Cell, error) {
	f.gotIndex = index
	return f.cell, f.err
}

func (f *fakeUsecase) DeleteCell(context.Context, int64, int64, int) error {
	return f.err
}

// fakeTokens понимает токены вида "access-<id>".
type fakeTokens struct{}

func (fakeTokens) Parse(token string) (int64, error) {
	return strconv.ParseInt(strings.TrimPrefix(token, "access-"), 10, 64)
}

type notebookHandler = Handler

type testServer struct {
	*userdelivery.Handler
	*notebookHandler
}

// newRouter собирает сервер как в main, вместе с валидатором.
func newRouter(t *testing.T, uc usecase.Usecase) http.Handler {
	t.Helper()
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	srv := testServer{userdelivery.NewHandler(nil, userdelivery.CookieConfig{}, nil), NewHandler(uc)}
	strict := api.NewStrictHandlerWithOptions(srv, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  httperr.RequestError,
		ResponseErrorHandlerFunc: httperr.ResponseError,
	})
	return api.HandlerWithOptions(strict, api.GorillaServerOptions{
		BaseURL:    "/api/v1",
		BaseRouter: mux.NewRouter(),
		Middlewares: []api.MiddlewareFunc{
			middleware.Validator(spec, "/api/v1"),
			middleware.Authenticate(fakeTokens{}),
		},
		ErrorHandlerFunc: httperr.RequestError,
	})
}

func do(t *testing.T, uc usecase.Usecase, method, path, body string, withToken bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if withToken {
		req.AddCookie(&http.Cookie{Name: "access_token", Value: "access-1"})
	}
	rec := httptest.NewRecorder()
	newRouter(t, uc).ServeHTTP(rec, req)
	return rec
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		noToken    bool
		err        error
		wantStatus int
		wantCode   api.ErrorCode
	}{
		{name: "список без токена", method: http.MethodGet, path: "/notebooks", noToken: true,
			wantStatus: http.StatusUnauthorized, wantCode: api.Unauthorized},
		{name: "удаление без токена", method: http.MethodDelete, path: "/notebooks/1/cells/0", noToken: true,
			wantStatus: http.StatusUnauthorized, wantCode: api.Unauthorized},
		{name: "id не число", method: http.MethodGet, path: "/notebooks/abc",
			wantStatus: http.StatusBadRequest, wantCode: api.ValidationError},
		{name: "id меньше 1", method: http.MethodGet, path: "/notebooks/0",
			wantStatus: http.StatusBadRequest, wantCode: api.ValidationError},
		{name: "kind не из enum", method: http.MethodPost, path: "/notebooks/1/cells", body: `{"kind":"python"}`,
			wantStatus: http.StatusBadRequest, wantCode: api.ValidationError},
		{name: "лишнее поле в CreateCellRequest", method: http.MethodPost, path: "/notebooks/1/cells",
			body:       `{"kind":"code","source":"print(1)"}`,
			wantStatus: http.StatusBadRequest, wantCode: api.ValidationError},
		{name: "пустое тело", method: http.MethodPost, path: "/notebooks", body: `{}`,
			wantStatus: http.StatusBadRequest, wantCode: api.ValidationError},

		{name: "имя из пробелов", method: http.MethodPost, path: "/notebooks", body: `{"name":"   "}`,
			err: usecase.ErrEmptyName, wantStatus: http.StatusBadRequest, wantCode: api.ValidationError},
		{name: "чужой блокнот", method: http.MethodGet, path: "/notebooks/1",
			err: usecase.ErrNotebookNotFound, wantStatus: http.StatusNotFound, wantCode: api.NotFound},
		{name: "блок в чужой блокнот", method: http.MethodPost, path: "/notebooks/1/cells", body: `{"kind":"code"}`,
			err: usecase.ErrNotebookNotFound, wantStatus: http.StatusNotFound, wantCode: api.NotFound},
		{name: "index за концом", method: http.MethodPost, path: "/notebooks/1/cells", body: `{"kind":"code","index":5}`,
			err: usecase.ErrIndexOutOfRange, wantStatus: http.StatusBadRequest, wantCode: api.ValidationError},
		{name: "предел блоков", method: http.MethodPost, path: "/notebooks/1/cells", body: `{"kind":"code"}`,
			err: usecase.ErrTooManyCells, wantStatus: http.StatusConflict, wantCode: api.ValidationError},
		{name: "удаление несуществующего блока", method: http.MethodDelete, path: "/notebooks/1/cells/7",
			err: usecase.ErrCellNotFound, wantStatus: http.StatusNotFound, wantCode: api.NotFound},
		{name: "S3 недоступен", method: http.MethodGet, path: "/notebooks/1",
			err: errors.New("s3 is down"), wantStatus: http.StatusInternalServerError, wantCode: api.Internal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, &fakeUsecase{err: tt.err}, tt.method, tt.path, tt.body, !tt.noToken)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.wantStatus, rec.Body)
			}
			var got api.Error
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("тело не в формате Error: %v; body %s", err, rec.Body)
			}
			if got.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", got.Code, tt.wantCode)
			}
		})
	}
}

func TestListNotebooks(t *testing.T) {
	updated := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		list []models.NotebookSummary
		want string
	}{
		{name: "пустой список — [], а не null", list: nil, want: `[]`},
		{
			name: "один блокнот",
			list: []models.NotebookSummary{{ID: 3, Name: "a", CellsCount: 2, UpdatedAt: updated}},
			want: `[{"cells_count":2,"id":3,"name":"a","updated_at":"2026-10-04T12:00:00Z"}]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, &fakeUsecase{list: tt.list}, http.MethodGet, "/notebooks", "", true)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tt.want {
				t.Errorf("body = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestCreateAndGetNotebook(t *testing.T) {
	created := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	uc := &fakeUsecase{notebook: models.Notebook{
		ID: 3, Name: "a", CreatedAt: created, UpdatedAt: created,
		Cells: []models.Cell{{ID: "c1", Kind: models.CellKindCode}},
	}}
	want := api.Notebook{
		Id: 3, Name: "a", CreatedAt: created, UpdatedAt: created,
		Cells: []api.Cell{{Id: "c1", Kind: api.CellKindCode, Source: ""}},
	}

	for _, req := range []struct {
		method, path, body string
		wantStatus         int
	}{
		{method: http.MethodPost, path: "/notebooks", body: `{"name":"a"}`, wantStatus: http.StatusCreated},
		{method: http.MethodGet, path: "/notebooks/3", wantStatus: http.StatusOK},
	} {
		rec := do(t, uc, req.method, req.path, req.body, true)
		if rec.Code != req.wantStatus {
			t.Fatalf("%s %s: status = %d, want %d; body %s", req.method, req.path, rec.Code, req.wantStatus, rec.Body)
		}
		var got api.Notebook
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s %s (-want +got):\n%s", req.method, req.path, diff)
		}
	}
}

func TestCreateCell(t *testing.T) {
	uc := &fakeUsecase{cell: models.Cell{ID: "c2", Kind: models.CellKindMarkdown}}
	rec := do(t, uc, http.MethodPost, "/notebooks/3/cells", `{"kind":"markdown","index":0}`, true)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rec.Code, rec.Body)
	}
	var got api.Cell
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(api.Cell{Id: "c2", Kind: api.CellKindMarkdown}, got); diff != "" {
		t.Errorf("тело ответа (-want +got):\n%s", diff)
	}
	if uc.gotIndex == nil || *uc.gotIndex != 0 {
		t.Errorf("index = %v, want 0: хендлер не передал его в usecase", uc.gotIndex)
	}
}

func TestDeleteCell(t *testing.T) {
	rec := do(t, &fakeUsecase{}, http.MethodDelete, "/notebooks/3/cells/0", "", true)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body %s", rec.Code, rec.Body)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("у 204 есть тело: %s", rec.Body)
	}
}
