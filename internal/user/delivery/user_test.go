package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/httperr"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/middleware"
	notebookdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/models"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/mux"
)

// fakeUsecase отдаёт заранее заданный результат: хендлер проверяется
// отдельно от бизнес-логики.
type fakeUsecase struct {
	user models.User
	sess models.Session
	err  error
	// gotID запоминает, какой id хендлер достал из контекста.
	gotID int64
}

var _ usecase.Usecase = (*fakeUsecase)(nil)

func (f *fakeUsecase) Register(context.Context, string, string) (models.User, models.Session, error) {
	return f.user, f.sess, f.err
}

func (f *fakeUsecase) Login(context.Context, string, string) (models.User, models.Session, error) {
	return f.user, f.sess, f.err
}

func (f *fakeUsecase) Refresh(context.Context, string) (models.Session, error) {
	return f.sess, f.err
}

func (f *fakeUsecase) Logout(context.Context, string) error {
	return f.err
}

func (f *fakeUsecase) CurrentUser(_ context.Context, id int64) (models.User, error) {
	f.gotID = id
	return f.user, f.err
}

// fakeTokens понимает токены вида "access-<id>".
type fakeTokens struct{}

func (fakeTokens) Parse(token string) (int64, error) {
	return strconv.ParseInt(strings.TrimPrefix(token, "access-"), 10, 64)
}

// Встроить два типа с именем Handler нельзя, поэтому псевдоним.
type userHandler = Handler

type testServer struct {
	*userHandler
	*notebookdelivery.Handler
}

// newRouter собирает сервер как в main, но без валидатора:
// проверяется хендлер вместе со сгенерированным кодом и httperr.
func newRouter(uc usecase.Usecase) http.Handler {
	h := NewHandler(uc, CookieConfig{AccessPath: "/api/v1", RefreshPath: "/api/v1/auth", AccessTTL: 15 * time.Minute})
	strict := api.NewStrictHandlerWithOptions(testServer{h, notebookdelivery.NewHandler(nil)}, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  httperr.RequestError,
		ResponseErrorHandlerFunc: httperr.ResponseError,
	})
	return api.HandlerWithOptions(strict, api.GorillaServerOptions{
		BaseURL:          "/api/v1",
		BaseRouter:       mux.NewRouter(),
		Middlewares:      []api.MiddlewareFunc{middleware.Authenticate(fakeTokens{})},
		ErrorHandlerFunc: httperr.RequestError,
	})
}

func newSession(userID int64) models.Session {
	return models.Session{
		UserID:       userID,
		AccessToken:  "access-" + strconv.FormatInt(userID, 10),
		RefreshToken: "new-refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
}

func TestHandlerErrors(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		refresh    string
		err        error
		wantStatus int
		want       api.Error
	}{
		{
			name: "регистрация: логин занят", method: http.MethodPost, path: "/auth/register",
			body: `{"login":"alice","password":"password123"}`, err: usecase.ErrLoginTaken,
			wantStatus: http.StatusConflict,
			want:       api.Error{Code: api.LoginTaken, Message: "login is already taken"},
		},
		{
			name: "регистрация: длинный пароль", method: http.MethodPost, path: "/auth/register",
			body: `{"login":"alice","password":"password123"}`, err: usecase.ErrPasswordTooLong,
			wantStatus: http.StatusBadRequest,
			want:       api.Error{Code: api.ValidationError, Message: "password is longer than 72 bytes"},
		},
		{
			name: "регистрация: невалидный JSON", method: http.MethodPost, path: "/auth/register",
			body:       `{"login":`,
			wantStatus: http.StatusBadRequest,
			want:       api.Error{Code: api.ValidationError, Message: "can't decode JSON body: unexpected EOF"},
		},
		{
			name: "регистрация: ошибка базы", method: http.MethodPost, path: "/auth/register",
			body: `{"login":"alice","password":"password123"}`, err: errors.New("connection refused"),
			wantStatus: http.StatusInternalServerError,
			want:       api.Error{Code: api.Internal, Message: "internal server error"},
		},
		{
			name: "вход: неверный пароль", method: http.MethodPost, path: "/auth/login",
			body: `{"login":"alice","password":"wrong-password"}`, err: usecase.ErrInvalidCredentials,
			wantStatus: http.StatusUnauthorized,
			want:       api.Error{Code: api.InvalidCredentials, Message: "invalid login or password"},
		},
		{
			name: "refresh без cookie", method: http.MethodPost, path: "/auth/refresh",
			wantStatus: http.StatusUnauthorized,
			want:       api.Error{Code: api.Unauthorized, Message: "refresh token is missing"},
		},
		{
			name: "refresh: чужой токен", method: http.MethodPost, path: "/auth/refresh",
			refresh: "stolen", err: usecase.ErrInvalidRefresh,
			wantStatus: http.StatusUnauthorized,
			want:       api.Error{Code: api.Unauthorized, Message: "refresh token is invalid or expired"},
		},
		{
			name: "logout: чужой токен", method: http.MethodPost, path: "/auth/logout",
			refresh: "stolen", err: usecase.ErrInvalidRefresh,
			wantStatus: http.StatusUnauthorized,
			want:       api.Error{Code: api.Unauthorized, Message: "refresh token is invalid or expired"},
		},
		{
			name: "профиль: пользователя удалили", method: http.MethodGet, path: "/users/me",
			err:        usecase.ErrUserNotFound,
			wantStatus: http.StatusUnauthorized,
			want:       api.Error{Code: api.Unauthorized, Message: "user no longer exists"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/v1"+tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(&http.Cookie{Name: "access_token", Value: "access-1"})
			if tt.refresh != "" {
				req.AddCookie(&http.Cookie{Name: "refresh_token", Value: tt.refresh})
			}
			rec := httptest.NewRecorder()

			newRouter(&fakeUsecase{err: tt.err}).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if len(rec.Header().Values("Set-Cookie")) != 0 || rec.Header().Get("Authorization") != "" {
				t.Error("error response issued tokens")
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
	u := models.User{ID: 1, Login: "bob"}
	uc := &fakeUsecase{user: u, sess: newSession(u.ID)}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register",
		strings.NewReader(`{"login":"bob","password":"password123"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	newRouter(uc).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rec.Code, rec.Body)
	}
	var got api.User
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(api.User{Id: u.ID, Login: "bob"}, got); diff != "" {
		t.Errorf("тело ответа (-want +got):\n%s", diff)
	}
	assertTokenCookies(t, rec, false, false)
}

func TestHandlerRefreshAndLogout(t *testing.T) {
	uc := &fakeUsecase{sess: newSession(1)}
	post := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1"+path, nil)
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: "old-refresh"})
		rec := httptest.NewRecorder()
		newRouter(uc).ServeHTTP(rec, req)
		return rec
	}

	rec := post("/auth/refresh")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("refresh: status = %d, want 204; body %s", rec.Code, rec.Body)
	}
	assertTokenCookies(t, rec, false, false)

	rec = post("/auth/logout")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: status = %d, want 204; body %s", rec.Code, rec.Body)
	}
	assertTokenCookies(t, rec, false, true)
}

func TestHandlerGetCurrentUser(t *testing.T) {
	u := models.User{ID: 1, Login: "alice"}
	uc := &fakeUsecase{user: u}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
	req.AddCookie(&http.Cookie{Name: "access_token", Value: "access-" + strconv.FormatInt(u.ID, 10)})
	rec := httptest.NewRecorder()

	newRouter(uc).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if uc.gotID != u.ID {
		t.Errorf("хендлер взял id %d, want %d из токена", uc.gotID, u.ID)
	}
	var got api.User
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(api.User{Id: u.ID, Login: "alice"}, got); diff != "" {
		t.Errorf("тело ответа (-want +got):\n%s", diff)
	}
}

func assertTokenCookies(t *testing.T, rec *httptest.ResponseRecorder, secure, deleted bool) {
	t.Helper()
	if rec.Header().Get("Authorization") != "" {
		t.Error("response exposes access token in Authorization")
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 2 || len(rec.Header().Values("Set-Cookie")) != 2 {
		t.Fatalf("expected two separate cookies, got %v", rec.Header().Values("Set-Cookie"))
	}
	for i, cookie := range cookies {
		name, path, value := "access_token", "/api/v1", "access-1"
		if i == 1 {
			name, path, value = "refresh_token", "/api/v1/auth", "new-refresh"
		}
		if deleted {
			value = ""
		}
		if cookie.Name != name || cookie.Path != path || cookie.Value != value || !cookie.HttpOnly || cookie.Secure != secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Domain != "" {
			t.Errorf("unexpected cookie: %+v", cookie)
		}
		switch {
		case deleted:
			if cookie.MaxAge != -1 {
				t.Errorf("cookie not deleted: %+v", cookie)
			}
		case i == 0:
			if cookie.MaxAge != 900 {
				t.Errorf("access MaxAge = %d, want 900", cookie.MaxAge)
			}
		case cookie.MaxAge < 3590 || cookie.MaxAge > 3600:
			t.Errorf("refresh MaxAge = %d", cookie.MaxAge)
		}
	}
}

func TestHandlerSessionCookies(t *testing.T) {
	for _, secure := range []bool{false, true} {
		for _, operation := range []string{"login", "register", "refresh", "logout"} {
			t.Run(operation+"/secure="+strconv.FormatBool(secure), func(t *testing.T) {
				uc := &fakeUsecase{user: models.User{ID: 1, Login: "bob"}, sess: newSession(1)}
				handler := NewHandler(uc, CookieConfig{AccessPath: "/api/v1", RefreshPath: "/api/v1/auth", AccessTTL: 15 * time.Minute, Secure: secure})
				strict := api.NewStrictHandlerWithOptions(testServer{handler, notebookdelivery.NewHandler(nil)}, nil, api.StrictHTTPServerOptions{})
				router := api.HandlerWithOptions(strict, api.GorillaServerOptions{BaseURL: "/api/v1"})
				req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/"+operation, strings.NewReader(`{"login":"bob","password":"password123"}`))
				req.Header.Set("Content-Type", "application/json")
				req.AddCookie(&http.Cookie{Name: "refresh_token", Value: "old-refresh"})
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				want := http.StatusNoContent
				if operation == "login" {
					want = http.StatusOK
				}
				if operation == "register" {
					want = http.StatusCreated
				}
				if rec.Code != want {
					t.Fatalf("status = %d, want %d: %s", rec.Code, want, rec.Body)
				}
				assertTokenCookies(t, rec, secure, operation == "logout")
			})
		}
	}
}

func TestHandlerCookieSessionLifecycle(t *testing.T) {
	uc := &fakeUsecase{user: models.User{ID: 1, Login: "bob"}, sess: newSession(1)}
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	router := middleware.Authenticate(fakeTokens{})(middleware.Validator(spec, "/api/v1")(newRouter(uc)))
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		method, path string
		status       int
	}{
		{http.MethodPost, "/auth/login", http.StatusOK},
		{http.MethodGet, "/users/me", http.StatusOK},
		{http.MethodPost, "/auth/logout", http.StatusNoContent},
		{http.MethodGet, "/users/me", http.StatusUnauthorized},
	} {
		req, err := http.NewRequest(step.method, "http://localhost/api/v1"+step.path, strings.NewReader(`{"login":"bob","password":"password123"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		for _, cookie := range jar.Cookies(req.URL) {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		response := rec.Result()
		jar.SetCookies(req.URL, response.Cookies())
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != step.status {
			t.Fatalf("%s: status = %d, want %d", step.path, response.StatusCode, step.status)
		}
		if step.path == "/auth/logout" && len(jar.Cookies(req.URL)) != 0 {
			t.Error("logout left cookies in the jar")
		}
	}
}
