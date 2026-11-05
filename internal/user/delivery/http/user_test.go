package delivery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	notebookdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery/http"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/observability"
	runtimedelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/delivery/http"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/httperr"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/middleware"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/security"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"github.com/google/go-cmp/cmp"
	"go.uber.org/mock/gomock"
)

// Встроить два типа с именем Handler нельзя, поэтому псевдоним.
type userHandler = Handler

// runtimeHandler позволяет встроить runtime-хендлер рядом с другими типами Handler без конфликта имён.
type runtimeHandler = runtimedelivery.Handler

// testServer объединяет обработчики API для тестового маршрутизатора.
type testServer struct {
	*runtimeHandler
	*userHandler
	*notebookdelivery.Handler
}

func newRouter(t *testing.T, uc usecase.Usecase) http.Handler {
	t.Helper()
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	strict := api.NewStrictHandlerWithOptions(testServer{userHandler: NewHandler(uc)}, nil, api.StrictHTTPServerOptions{RequestErrorHandlerFunc: httperr.RequestError, ResponseErrorHandlerFunc: httperr.ResponseError})
	return security.AuthenticateBearer(testTokens(t), allowSessions{})(api.HandlerWithOptions(strict, api.GorillaServerOptions{BaseURL: "/api/v1", Middlewares: []api.MiddlewareFunc{middleware.Validator(spec, "/api/v1")}}))
}

// newSession создаёт тестовую сессию авторизации с ID пользователя и сроками действия токенов.
func newSession(userID int64) usecase.Session {
	return usecase.Session{
		SessionClaims:   usecase.SessionClaims{UserID: userID},
		AccessToken:     "access-" + strconv.FormatInt(userID, 10),
		RefreshToken:    "new-refresh",
		ExpiresAt:       time.Now().Add(time.Hour),
		AccessExpiresAt: time.Now().Add(15 * time.Minute),
	}
}

func TestAuthErrors(t *testing.T) {
	for _, tc := range []struct {
		path, body string
		err        error
		status     int
	}{
		{"auth/register", `{"login":"bob","password":"password123"}`, usecase.ErrLoginTaken, 409},
		{"auth/register", `{"login":"bob","password":"password123"}`, usecase.ErrPasswordTooLong, 400},
		{"auth/login", `{"login":"bob","password":"password123"}`, usecase.ErrInvalidCredentials, 401},
		{"auth/refresh", `{"refresh_token":"refresh"}`, usecase.ErrInvalidRefresh, 401},
		{"auth/logout", `{"refresh_token":"refresh"}`, usecase.ErrInvalidRefresh, 401},
		{"users/me", "", usecase.ErrUserNotFound, 401},
		{"users/me", "", errors.New("database unavailable"), 500},
	} {
		t.Run(tc.path+tc.err.Error(), func(t *testing.T) {
			uc := NewMockUsecase(gomock.NewController(t))
			switch tc.path {
			case "auth/register":
				uc.EXPECT().Register(gomock.Any(), "bob", "password123").Return(domain.User{}, usecase.Session{}, tc.err)
			case "auth/login":
				uc.EXPECT().Login(gomock.Any(), "bob", "password123").Return(domain.User{}, usecase.Session{}, tc.err)
			case "auth/refresh":
				uc.EXPECT().Refresh(gomock.Any(), "refresh").Return(usecase.Session{}, tc.err)
			case "auth/logout":
				uc.EXPECT().Logout(gomock.Any(), "refresh", gomock.Any()).Return(tc.err)
			case "users/me":
				uc.EXPECT().CurrentUser(gomock.Any(), int64(42)).Return(domain.User{}, tc.err)
			}
			method := "POST"
			if tc.path == "users/me" {
				method = "GET"
			}
			req := httptest.NewRequest(method, "/api/v1/"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer access-42")
			req.Header.Set("X-BFF-Key", "test-key")
			rec := httptest.NewRecorder()
			newRouter(t, uc).ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.status, rec.Body)
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Fatal("browser cookies emitted")
			}
		})
	}
}
func TestHandlerLoginVerificationFailure(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	failure := errors.New("unsupported password hash version")
	uc := NewMockUsecase(gomock.NewController(t))
	uc.EXPECT().Login(gomock.Any(), "alice", "password123").Return(domain.User{}, usecase.Session{}, observability.WithCode(fmt.Errorf("compare password: %w", failure), "password_compare_failed"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"login":"alice","password":"password123"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BFF-Key", "test-key")
	rec := httptest.NewRecorder()
	newRouter(t, uc).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", rec.Code, rec.Body)
	}
	var got api.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := api.Error{Code: api.ErrorCodeInternal, Message: "internal server error"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("error response (-want +got):\n%s", diff)
	}
	if strings.Contains(rec.Body.String(), failure.Error()) {
		t.Error("response exposed verification failure")
	}
	if !strings.Contains(logs.String(), "password_compare_failed") || strings.Contains(logs.String(), failure.Error()) {
		t.Error("log must contain only a safe verification failure code")
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("verification failure issued browser cookies")
	}
}
