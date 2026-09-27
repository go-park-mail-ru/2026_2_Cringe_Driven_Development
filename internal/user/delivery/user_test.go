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

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/httperr"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/middleware"
	notebookdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/models"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// fakeUsecase отдаёт заранее заданный результат: хендлер проверяется
// отдельно от бизнес-логики.
type fakeUsecase struct {
	user models.User
	sess models.Session
	err  error
	// gotID запоминает, какой id хендлер достал из контекста.
	gotID uuid.UUID
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

func (f *fakeUsecase) CurrentUser(_ context.Context, id uuid.UUID) (models.User, error) {
	f.gotID = id
	return f.user, f.err
}

// fakeTokens понимает токены вида "access-<uuid>".
type fakeTokens struct{}

func (fakeTokens) Parse(token string) (uuid.UUID, error) {
	return uuid.Parse(strings.TrimPrefix(token, "access-"))
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
	h := NewHandler(uc, CookieConfig{Path: "/api/v1/auth"})
	strict := api.NewStrictHandlerWithOptions(testServer{h, notebookdelivery.NewHandler()}, nil, api.StrictHTTPServerOptions{
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

func newSession(userID uuid.UUID) models.Session {
	return models.Session{
		UserID:       userID,
		AccessToken:  "access-" + userID.String(),
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
			req.Header.Set("Authorization", "Bearer access-"+uuid.NewString())
			if tt.refresh != "" {
				req.AddCookie(&http.Cookie{Name: "refresh_token", Value: tt.refresh})
			}
			rec := httptest.NewRecorder()

			newRouter(&fakeUsecase{err: tt.err}).ServeHTTP(rec, req)

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
	u := models.User{ID: uuid.New(), Login: "bob"}
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
	if want := "Bearer access-" + u.ID.String(); rec.Header().Get("Authorization") != want {
		t.Errorf("Authorization = %q, want %q", rec.Header().Get("Authorization"), want)
	}
	cookie := rec.Header().Get("Set-Cookie")
	for _, part := range []string{"refresh_token=new-refresh", "Path=/api/v1/auth", "HttpOnly", "SameSite=Lax"} {
		if !strings.Contains(cookie, part) {
			t.Errorf("Set-Cookie = %q, нет %q", cookie, part)
		}
	}
}

func TestHandlerRefreshAndLogout(t *testing.T) {
	uc := &fakeUsecase{sess: newSession(uuid.New())}
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
	if cookie := rec.Header().Get("Set-Cookie"); !strings.Contains(cookie, "refresh_token=new-refresh") {
		t.Errorf("refresh не выдал новый токен: Set-Cookie = %q", cookie)
	}

	rec = post("/auth/logout")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: status = %d, want 204; body %s", rec.Code, rec.Body)
	}
	if cookie := rec.Header().Get("Set-Cookie"); !strings.Contains(cookie, "Max-Age=0") {
		t.Errorf("logout не удалил cookie: Set-Cookie = %q", cookie)
	}
}

func TestHandlerGetCurrentUser(t *testing.T) {
	u := models.User{ID: uuid.New(), Login: "alice"}
	uc := &fakeUsecase{user: u}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
	req.Header.Set("Authorization", "Bearer access-"+u.ID.String())
	rec := httptest.NewRecorder()

	newRouter(uc).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if uc.gotID != u.ID {
		t.Errorf("хендлер взял id %s, want %s из токена", uc.gotID, u.ID)
	}
	var got api.User
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(api.User{Id: u.ID, Login: "alice"}, got); diff != "" {
		t.Errorf("тело ответа (-want +got):\n%s", diff)
	}
}
