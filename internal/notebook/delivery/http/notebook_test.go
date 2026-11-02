package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/httperr"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/middleware"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/security"
	userdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/delivery/http"
	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/mux"
	"go.uber.org/mock/gomock"
)

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
	srv := testServer{userdelivery.NewHandler(nil), NewHandler(uc)}
	strict := api.NewStrictHandlerWithOptions(srv, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  httperr.RequestError,
		ResponseErrorHandlerFunc: httperr.ResponseError,
	})
	return api.HandlerWithOptions(strict, api.GorillaServerOptions{
		BaseURL:    "/api/v1",
		BaseRouter: mux.NewRouter(),
		Middlewares: []api.MiddlewareFunc{
			middleware.Validator(spec, "/api/v1"),
			security.AuthenticateBearer(testTokens(t), allowSessions{}),
		},
		ErrorHandlerFunc: httperr.RequestError,
	})
}

type notebookRequestKey struct{}

func do(t *testing.T, uc usecase.Usecase, method, path, body string, withToken bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	req.Header.Set("X-BFF-Key", "test-key")
	req.Header.Set("If-Match", `"hash"`)
	req = req.WithContext(context.WithValue(req.Context(), notebookRequestKey{}, t.Name()))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if withToken {
		req.Header.Set("Authorization", "Bearer access-1")
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
			wantStatus: http.StatusUnauthorized, wantCode: api.ErrorCodeUnauthorized},
		{name: "удаление без токена", method: http.MethodDelete, path: "/notebooks/1/cells/0", noToken: true,
			wantStatus: http.StatusUnauthorized, wantCode: api.ErrorCodeUnauthorized},
		{name: "id не число", method: http.MethodGet, path: "/notebooks/abc",
			wantStatus: http.StatusBadRequest, wantCode: api.ErrorCodeValidationError},
		{name: "id меньше 1", method: http.MethodGet, path: "/notebooks/0",
			wantStatus: http.StatusBadRequest, wantCode: api.ErrorCodeValidationError},
		{name: "kind не из enum", method: http.MethodPost, path: "/notebooks/1/cells", body: `{"kind":"python"}`,
			wantStatus: http.StatusBadRequest, wantCode: api.ErrorCodeValidationError},
		{name: "лишнее поле в CreateCellRequest", method: http.MethodPost, path: "/notebooks/1/cells",
			body:       `{"kind":"code","source":"print(1)"}`,
			wantStatus: http.StatusBadRequest, wantCode: api.ErrorCodeValidationError},
		{name: "пустое тело", method: http.MethodPost, path: "/notebooks", body: `{}`,
			wantStatus: http.StatusBadRequest, wantCode: api.ErrorCodeValidationError},

		{name: "имя из пробелов", method: http.MethodPost, path: "/notebooks", body: `{"name":"   "}`,
			err: usecase.ErrEmptyName, wantStatus: http.StatusBadRequest, wantCode: api.ErrorCodeValidationError},
		{name: "чужой блокнот", method: http.MethodGet, path: "/notebooks/1",
			err: usecase.ErrNotebookNotFound, wantStatus: http.StatusNotFound, wantCode: api.ErrorCodeNotFound},
		{name: "блок в чужой блокнот", method: http.MethodPost, path: "/notebooks/1/cells", body: `{"kind":"code"}`,
			err: usecase.ErrNotebookNotFound, wantStatus: http.StatusNotFound, wantCode: api.ErrorCodeNotFound},
		{name: "index за концом", method: http.MethodPost, path: "/notebooks/1/cells", body: `{"kind":"code","index":5}`,
			err: usecase.ErrIndexOutOfRange, wantStatus: http.StatusBadRequest, wantCode: api.ErrorCodeValidationError},
		{name: "предел блоков", method: http.MethodPost, path: "/notebooks/1/cells", body: `{"kind":"code"}`,
			err: usecase.ErrTooManyCells, wantStatus: http.StatusConflict, wantCode: api.ErrorCodeValidationError},
		{name: "удаление несуществующего блока", method: http.MethodDelete, path: "/notebooks/1/cells/7",
			err: usecase.ErrCellNotFound, wantStatus: http.StatusNotFound, wantCode: api.ErrorCodeNotFound},
		{name: "S3 недоступен", method: http.MethodGet, path: "/notebooks/1",
			err: errors.New("s3 is down"), wantStatus: http.StatusInternalServerError, wantCode: api.ErrorCodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uc := NewMockUsecase(gomock.NewController(t))
			if tt.err != nil {
				switch {
				case tt.method == http.MethodGet:
					uc.EXPECT().Get(gomock.Any(), int64(1), int64(1)).Return(domain.Notebook{}, tt.err)
				case tt.method == http.MethodDelete:
					uc.EXPECT().DeleteCell(gomock.Any(), int64(1), int64(1), "hash", 7).Return(tt.err)
				case tt.path == "/notebooks":
					uc.EXPECT().Create(gomock.Any(), int64(1), "   ").Return(domain.Notebook{}, tt.err)
				default:
					var body api.CreateCellRequest
					if err := json.Unmarshal([]byte(tt.body), &body); err != nil {
						t.Fatal(err)
					}
					uc.EXPECT().CreateCell(gomock.Any(), int64(1), int64(1), "hash", domain.CellKind(body.Kind), body.Index).Return(domain.Cell{}, tt.err)
				}
			}
			rec := do(t, uc, tt.method, tt.path, tt.body, !tt.noToken)

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
		list []domain.NotebookSummary
		want string
	}{
		{name: "пустой список — [], а не null", list: nil, want: `[]`},
		{
			name: "один блокнот",
			list: []domain.NotebookSummary{{ID: 3, Name: "a", CellsCount: 2, UpdatedAt: updated}},
			want: `[{"cells_count":2,"id":3,"name":"a","updated_at":"2026-10-04T12:00:00Z"}]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uc := NewMockUsecase(gomock.NewController(t))
			uc.EXPECT().List(gomock.Any(), int64(1)).Return(tt.list, nil)
			rec := do(t, uc, http.MethodGet, "/notebooks", "", true)

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
	notebook := domain.Notebook{
		ID: 3, Name: "a", CreatedAt: created, UpdatedAt: created,
		Cells: []domain.Cell{{ID: "c1", Kind: domain.CellKindCode}},
	}
	uc := NewMockUsecase(gomock.NewController(t))
	uc.EXPECT().Create(gomock.Any(), int64(1), "a").Return(notebook, nil)
	uc.EXPECT().Get(gomock.Any(), int64(3), int64(1)).Return(notebook, nil)
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
	uc := NewMockUsecase(gomock.NewController(t))
	index := 0
	uc.EXPECT().CreateCell(gomock.Any(), int64(3), int64(1), "hash", domain.CellKindMarkdown, &index).Return(domain.Cell{ID: "c2", Kind: domain.CellKindMarkdown}, nil)
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

}

func TestDeleteCell(t *testing.T) {
	uc := NewMockUsecase(gomock.NewController(t))
	uc.EXPECT().DeleteCell(gomock.Any(), int64(3), int64(1), "hash", 0).Return(nil)
	rec := do(t, uc, http.MethodDelete, "/notebooks/3/cells/0", "", true)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body %s", rec.Code, rec.Body)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("у 204 есть тело: %s", rec.Body)
	}
}

func testTokens(t *testing.T) *MockTokenParser {
	t.Helper()
	tokens := NewMockTokenParser(gomock.NewController(t))
	// Routing helpers may receive requests with or without a cookie.
	tokens.EXPECT().Parse("access-1").Return(testClaims(1), nil).AnyTimes()
	return tokens
}
