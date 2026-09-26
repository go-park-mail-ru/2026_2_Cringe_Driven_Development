package user

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/httperr"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/middleware"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook"
	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Parse у fakeTokens — обратная к Issue, чтобы работал middleware.Authenticate.
func (fakeTokens) Parse(token string) (uuid.UUID, error) {
	return uuid.Parse(strings.TrimPrefix(token, "access-"))
}

// Встроить два типа с именем Handler нельзя, поэтому псевдоним.
type userHandler = Handler

type testServer struct {
	*userHandler
	*notebook.Handler
}

type fixture struct {
	router  http.Handler
	user    User
	refresh string
}

// newFixture собирает сервер как в main, но без базы и без валидатора:
// проверяем хендлер вместе со сгенерированным кодом и httperr.
func newFixture(t *testing.T) fixture {
	t.Helper()
	repo := newFakeRepo()
	u := repo.addUser("alice", "password123")
	svc := NewService(repo, fakeTokens{}, refreshTTL)
	_, sess, err := svc.Login(t.Context(), "alice", "password123")
	if err != nil {
		t.Fatal(err)
	}

	h := NewHandler(svc, CookieConfig{Path: "/api/v1/auth"})
	strict := api.NewStrictHandlerWithOptions(testServer{h, notebook.NewHandler()}, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  httperr.RequestError,
		ResponseErrorHandlerFunc: httperr.ResponseError,
	})
	router := api.HandlerWithOptions(strict, api.GorillaServerOptions{
		BaseURL:          "/api/v1",
		BaseRouter:       mux.NewRouter(),
		Middlewares:      []api.MiddlewareFunc{middleware.Authenticate(fakeTokens{})},
		ErrorHandlerFunc: httperr.RequestError,
	})
	return fixture{router: router, user: u, refresh: sess.RefreshToken}
}

func TestHandlerErrors(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		cookie     bool
		wantStatus int
		want       api.Error
	}{
		{
			name: "регистрация: логин занят", path: "/auth/register",
			body:       `{"login":"Alice","password":"password123"}`,
			wantStatus: http.StatusConflict,
			want:       api.Error{Code: api.LoginTaken, Message: "login is already taken"},
		},
		{
			name: "регистрация: невалидный JSON", path: "/auth/register",
			body:       `{"login":`,
			wantStatus: http.StatusBadRequest,
			want:       api.Error{Code: api.ValidationError, Message: "can't decode JSON body: unexpected EOF"},
		},
		{
			name: "вход: неверный пароль", path: "/auth/login",
			body:       `{"login":"alice","password":"wrong-password"}`,
			wantStatus: http.StatusUnauthorized,
			want:       api.Error{Code: api.InvalidCredentials, Message: "invalid login or password"},
		},
		{
			name: "refresh без cookie", path: "/auth/refresh",
			wantStatus: http.StatusUnauthorized,
			want:       api.Error{Code: api.Unauthorized, Message: "refresh token is missing"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			req := httptest.NewRequest(http.MethodPost, "/api/v1"+tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			f.router.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.wantStatus, rec.Body)
			}
			var got api.Error
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("тело не в формате Error: %v; body %s", err, rec.Body)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("тело ответа (-want +got):\n%s", diff)
			}
		})
	}
}

func TestHandlerRegister(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register",
		strings.NewReader(`{"login":"bob","password":"password123"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	f.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rec.Code, rec.Body)
	}
	var got api.User
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if want := "Bearer access-" + got.Id.String(); rec.Header().Get("Authorization") != want {
		t.Errorf("Authorization = %q, want %q", rec.Header().Get("Authorization"), want)
	}
	cookie := rec.Header().Get("Set-Cookie")
	for _, part := range []string{"refresh_token=", "Path=/api/v1/auth", "HttpOnly", "SameSite=Lax"} {
		if !strings.Contains(cookie, part) {
			t.Errorf("Set-Cookie = %q, нет %q", cookie, part)
		}
	}
}

func TestHandlerRefreshAndLogout(t *testing.T) {
	f := newFixture(t)
	post := func(path, refresh string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1"+path, nil)
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: refresh})
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)
		return rec
	}

	rec := post("/auth/refresh", f.refresh)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("refresh: status = %d, want 204; body %s", rec.Code, rec.Body)
	}
	newRefresh := (&http.Response{Header: rec.Header()}).Cookies()[0].Value

	if rec := post("/auth/logout", newRefresh); rec.Code != http.StatusNoContent {
		t.Fatalf("logout: status = %d, want 204; body %s", rec.Code, rec.Body)
	} else if cookie := rec.Header().Get("Set-Cookie"); !strings.Contains(cookie, "Max-Age=0") {
		t.Errorf("logout не удалил cookie: Set-Cookie = %q", cookie)
	}

	if rec := post("/auth/refresh", newRefresh); rec.Code != http.StatusUnauthorized {
		t.Errorf("refresh после logout: status = %d, want 401", rec.Code)
	}
}

func TestHandlerGetCurrentUser(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
	req.Header.Set("Authorization", "Bearer access-"+f.user.ID.String())
	rec := httptest.NewRecorder()

	f.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var got api.User
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(api.User{Id: f.user.ID, Login: "alice"}, got); diff != "" {
		t.Errorf("тело ответа (-want +got):\n%s", diff)
	}
}
