package httptransport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	notebookdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery/http"
	userredis "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/redis"
	usersecurity "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	userdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/delivery/http"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

type checkedSessions struct {
	usecase.SessionStore
	calls atomic.Int32
	fail  atomic.Bool
}

func (s *checkedSessions) ValidateAccess(ctx context.Context, c usecase.SessionClaims) error {
	s.calls.Add(1)
	if s.fail.Load() {
		return errors.New("redis unavailable")
	}
	return s.SessionStore.ValidateAccess(ctx, c)
}
func TestHTTPImmediateRevocationAcrossInstances(t *testing.T) {
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("set REDIS_TEST_ADDR for HTTP Redis integration")
	}
	ctx := context.Background()
	prefix := "test:" + uuid.NewString() + ":"
	clients := []*goredis.Client{goredis.NewClient(&goredis.Options{Addr: addr, Password: os.Getenv("REDIS_TEST_PASSWORD"), MaxRetries: -1}), goredis.NewClient(&goredis.Options{Addr: addr, Password: os.Getenv("REDIS_TEST_PASSWORD"), MaxRetries: -1})}
	if err := clients[0].Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		keys, err := clients[0].Keys(ctx, prefix+"*").Result()
		if err != nil {
			t.Error(err)
		}
		if len(keys) > 0 {
			_ = clients[0].Del(ctx, keys...).Err()
		}
		for _, c := range clients {
			_ = c.Close()
		}
	})
	stores := []*userredis.Store{userredis.NewStore(clients[0], prefix, time.Second), userredis.NewStore(clients[1], prefix, time.Second)}
	checker := &checkedSessions{SessionStore: stores[1]}
	tokens := usersecurity.NewAccessToken([]byte("secret"), time.Minute)
	issue := func(id int64) (string, string, usecase.SessionClaims) {
		t.Helper()
		v, err := stores[0].CurrentVersion(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		c := usecase.SessionClaims{UserID: id, SessionID: uuid.NewString(), Version: v}
		access, expiry, err := tokens.IssueAccess(c)
		if err != nil {
			t.Fatal(err)
		}
		refresh, hash := usersecurity.NewRefreshToken()
		err = stores[0].Create(ctx, usecase.NewSession{RefreshSession: usecase.RefreshSession{SessionClaims: c, TokenHash: hash, ExpiresAt: time.Now().Add(time.Hour)}, AccessExpiresAt: expiry})
		if err != nil {
			t.Fatal(err)
		}
		return access, refresh, c
	}
	access, refresh, claims := issue(1)
	device, deviceRefresh, _ := issue(1)
	other, _, _ := issue(2)
	routers := make([]http.Handler, 2, 3)
	for i, s := range stores {
		// CurrentUser здесь фикстура, а logout/refresh используют настоящий usecase.
		uc := usecase.NewUserUsecase(nil, s, tokens, nil, usersecurity.RefreshTokens{}, time.Hour)
		srv := Server{UserHandler: userdelivery.NewHandler(&sessionHTTPUsecase{UserUsecase: uc}), NotebookHandler: notebookdelivery.NewHandler(bffNotebooks{})}
		var err error
		if i == 1 {
			routers[i], err = NewRouter(srv, tokens, checker, "bff")
		} else {
			routers[i], err = NewRouter(srv, tokens, s, "bff")
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	request := func(instance int, method, path, access, body, key string) int {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.Header.Set("X-BFF-Key", key)
		r.Header.Set("Content-Type", "application/json")
		if access != "" {
			r.Header.Set("Authorization", "Bearer "+access)
		}
		w := httptest.NewRecorder()
		routers[instance].ServeHTTP(w, r)
		if w.Code == http.StatusNoContent && w.Body.Len() != 0 {
			t.Fatal("logout response must have no body")
		}
		return w.Code
	}
	expect := func(got, want int) {
		t.Helper()
		if got != want {
			t.Fatalf("HTTP %d, want %d", got, want)
		}
	}
	expect(request(1, "GET", "/users/me", access, "", "wrong"), 403)
	if checker.calls.Load() != 0 {
		t.Fatal("S2S reached Redis")
	}
	expect(request(1, "GET", "/users/me", "not.jwt", "", "bff"), 401)
	if checker.calls.Load() != 0 {
		t.Fatal("invalid JWT reached Redis")
	}
	for range 2 {
		expect(request(1, "GET", "/users/me", access, "", "bff"), 200)
	}
	if checker.calls.Load() != 2 {
		t.Fatal("authorization cached")
	}
	expect(request(1, "POST", "/notebooks", access, `{"name":"Notebook"}`, "bff"), 201)
	checker.fail.Store(true)
	expect(request(1, "GET", "/users/me", access, "", "bff"), 500)
	expect(request(1, "POST", "/notebooks", access, `{"name":"Notebook"}`, "bff"), 500)
	healthChecks := checker.calls.Load()
	healthReq := httptest.NewRequest("GET", "/health", nil)
	healthReq.Header.Set("Authorization", "Bearer "+access)
	healthResp := httptest.NewRecorder()
	routers[1].ServeHTTP(healthResp, healthReq)
	expect(healthResp.Code, 200)
	if checker.calls.Load() != healthChecks {
		t.Fatal("health touched Redis")
	}
	checker.fail.Store(false)
	expect(request(0, "POST", "/auth/logout", access, `{"refresh_token":"`+deviceRefresh+`"}`, "bff"), 401)
	expect(request(1, "GET", "/users/me", device, "", "bff"), 200)
	expect(request(0, "POST", "/auth/logout", access, `{"refresh_token":"`+refresh+`"}`, "bff"), 204)
	expect(request(1, "GET", "/users/me", access, "", "bff"), 401)
	expect(request(1, "POST", "/notebooks", access, `{"name":"Notebook"}`, "bff"), 401)
	expect(request(0, "POST", "/auth/logout", access, `{"refresh_token":"`+refresh+`"}`, "bff"), 401)
	expect(request(1, "POST", "/auth/refresh", "", `{"refresh_token":"`+refresh+`"}`, "bff"), 401)
	expect(request(1, "GET", "/users/me", device, "", "bff"), 200)
	anotherDevice, anotherRefresh, _ := issue(claims.UserID)
	expect(request(0, "POST", "/auth/logout-all", device, "", "bff"), 204)
	expect(request(1, "GET", "/users/me", device, "", "bff"), 401)
	expect(request(1, "GET", "/users/me", anotherDevice, "", "bff"), 401)
	expect(request(0, "POST", "/auth/logout-all", device, "", "bff"), 401)
	expect(request(1, "POST", "/auth/refresh", "", `{"refresh_token":"`+deviceRefresh+`"}`, "bff"), 401)
	expect(request(1, "POST", "/auth/refresh", "", `{"refresh_token":"`+anotherRefresh+`"}`, "bff"), 401)
	expect(request(1, "GET", "/users/me", other, "", "bff"), 200)
	fresh, _, _ := issue(1)
	expect(request(1, "GET", "/users/me", fresh, "", "bff"), 200)
	// Авторизация, проверенная до завершения отзыва, может пройти; последующие проверки должны падать.
	inFlight, _, _ := issue(3)
	entered, release := make(chan struct{}), make(chan struct{})
	blocked, err := NewRouter(Server{UserHandler: userdelivery.NewHandler(&blockedCurrentUser{entered: entered, release: release})}, tokens, stores[1], "bff")
	if err != nil {
		t.Fatal(err)
	}
	routers = append(routers, blocked)
	result := make(chan int, 1)
	go func() { result <- request(2, "GET", "/users/me", inFlight, "", "bff") }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not enter handler")
	}
	if err := stores[0].RevokeAll(ctx, 3); err != nil {
		close(release)
		t.Fatal(err)
	}
	denied := request(1, "GET", "/users/me", inFlight, "", "bff")
	close(release)
	expect(denied, 401)
	expect(<-result, 200)

}

type sessionHTTPUsecase struct{ *usecase.UserUsecase }

func (*sessionHTTPUsecase) CurrentUser(_ context.Context, id int64) (domain.User, error) {
	return domain.User{ID: id, Login: "alice"}, nil
}

type blockedCurrentUser struct {
	usecase.Usecase
	entered, release chan struct{}
}

func (u *blockedCurrentUser) CurrentUser(_ context.Context, id int64) (domain.User, error) {
	close(u.entered)
	<-u.release
	return domain.User{ID: id, Login: "alice"}, nil
}
