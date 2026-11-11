package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	userdb "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/postgres"
	userredis "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/redis"
	usersecurity "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	userdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/delivery/http"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pquerna/otp/totp"
	goredis "github.com/redis/go-redis/v9"
)

func TestTwoFactorHTTPPostgresRedis(t *testing.T) {
	dsn, addr := os.Getenv("TEST_POSTGRES_DSN"), os.Getenv("REDIS_TEST_ADDR")
	if dsn == "" || addr == "" {
		t.Skip("set TEST_POSTGRES_DSN and REDIS_TEST_ADDR")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := pgx.Identifier{"twofactor_" + uuid.NewString()}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, dropErr := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		if dropErr != nil {
			t.Error(dropErr)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	client := goredis.NewClient(&goredis.Options{Addr: addr, Password: os.Getenv("REDIS_TEST_PASSWORD"), MaxRetries: -1})
	prefix := "test:twofactor:" + uuid.NewString() + ":"
	t.Cleanup(func() {
		keys, scanErr := client.Keys(ctx, prefix+"*").Result()
		if scanErr != nil {
			t.Error(scanErr)
		} else if len(keys) > 0 {
			if deleteErr := client.Del(ctx, keys...).Err(); deleteErr != nil {
				t.Error(deleteErr)
			}
		}
		_ = client.Close()
	})
	sessions := userredis.NewStore(client, prefix, time.Second)
	tokens := usersecurity.NewAccessToken([]byte("integration-test-signing-key"), time.Minute)
	crypto, err := usersecurity.NewTOTP(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	uc := usecase.NewUserUsecase(userdb.NewUserRepository(pool), sessions, tokens, usersecurity.PasswordHasher{}, usersecurity.RefreshTokens{}, time.Hour, usecase.WithTwoFactor(crypto, userredis.NewAttemptLimiter(client, prefix, time.Second)))
	u, initial, err := uc.Register(ctx, "alice", "password123")
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(Server{UserHandler: userdelivery.NewHandler(uc)}, tokens, sessions, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })
	request := func(path string, body any, authorized bool) *httptest.ResponseRecorder {
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1"+path, bytes.NewReader(data))
		req.Header.Set("X-BFF-Key", "test-key")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if authorized {
			req.Header.Set("Authorization", "Bearer "+initial.AccessToken)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	expect := func(path string, body any, auth bool, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := request(path, body, auth)
		if w.Code != status {
			t.Fatalf("%s status=%d want=%d", path, w.Code, status)
		}
		return w
	}
	for _, path := range []string{"/auth/enable2fa", "/auth/confirm2fa", "/auth/disable2fa"} {
		expect(path, nil, false, 401)
	}
	expect("/auth/confirm2fa", map[string]string{"user_code": "012345"}, true, 400)
	// Независимые конкурентные запросы настройки должны сохранить ровно один секрет.
	results := make(chan usecase.TwoFactorSetup, 12)
	failures := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			setup, setupErr := uc.EnableTwoFactor(ctx, u.ID)
			if setupErr != nil {
				failures <- setupErr
			} else {
				results <- setup
			}
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	for failure := range failures {
		t.Fatal(failure)
	}
	var setup usecase.TwoFactorSetup
	for got := range results {
		if setup.Secret != "" && setup.Secret != got.Secret {
			t.Fatal("concurrent setup replaced secret")
		}
		setup = got
	}
	if setup.Secret == "" {
		t.Fatal("empty setup")
	}
	setupHTTP := expect("/auth/enable2fa", nil, true, 200)
	if setupHTTP.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("setup is cacheable")
	}
	var payload struct {
		Secret string `json:"secret"`
		QRCode string `json:"qr_code"`
	}
	if err = json.Unmarshal(setupHTTP.Body.Bytes(), &payload); err != nil || payload.Secret != setup.Secret || !strings.HasPrefix(payload.QRCode, "data:image/png;base64,") {
		t.Fatal("invalid setup JSON")
	}
	current, err := uc.CurrentUser(ctx, u.ID)
	if err != nil || current.Has2FA {
		t.Fatal("setup activated 2FA")
	}
	var stored []byte
	if err = pool.QueryRow(ctx, `SELECT totp_secret FROM users WHERE id=$1`, u.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte(setup.Secret)) {
		t.Fatal("plaintext persisted")
	}
	login := map[string]string{"login": "alice", "password": "password123"}
	expect("/auth/login", login, false, 200)
	for _, code := range []string{"12345", "１２３４５６", "123456\n"} {
		expect("/auth/confirm2fa", map[string]string{"user_code": code}, true, 400)
	}
	invalid := "000000"
	if _, matches := crypto.Match(setup.Secret, invalid, time.Now()); matches {
		invalid = "111111"
	}
	expect("/auth/confirm2fa", map[string]string{"user_code": invalid}, true, 401)
	code, err := totp.GenerateCode(setup.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	confirmation := make(chan int, 2)
	for range 2 {
		wg.Go(func() { confirmation <- request("/auth/confirm2fa", map[string]string{"user_code": code}, true).Code })
	}
	wg.Wait()
	close(confirmation)
	successes := 0
	for status := range confirmation {
		if status == 200 {
			successes++
		} else if status != 400 {
			t.Fatalf("concurrent confirm status=%d", status)
		}
	}
	if successes != 1 {
		t.Fatalf("confirm winners=%d", successes)
	}
	expect("/auth/enable2fa", nil, true, 400)
	expect("/auth/confirm2fa", map[string]string{"user_code": code}, true, 400)
	before := client.Keys(ctx, prefix+"auth_session:*").Val()
	expect("/auth/login", map[string]string{"login": "alice", "password": "wrongpassword", "user_code": code}, false, 400)
	expect("/auth/login", login, false, 412)
	expect("/auth/login", map[string]string{"login": "alice", "password": "password123", "user_code": ""}, false, 401)
	expect("/auth/login", map[string]string{"login": "alice", "password": "password123", "user_code": code}, false, 401)
	after := client.Keys(ctx, prefix+"auth_session:*").Val()
	if len(before) != len(after) {
		t.Fatal("failed TOTP created a session")
	}
	// Следующий шаг внутри допустимого окна сдвига; при конкурентном использовании один победитель.
	nextCode, err := totp.GenerateCode(setup.Secret, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	logins := make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			logins <- request("/auth/login", map[string]string{"login": "alice", "password": "password123", "user_code": nextCode}, false).Code
		})
	}
	wg.Wait()
	close(logins)
	successes = 0
	for status := range logins {
		if status == 200 {
			successes++
		} else if status != 401 {
			t.Fatalf("concurrent login status=%d", status)
		}
	}
	if successes != 1 {
		t.Fatalf("login winners=%d", successes)
	}
	expect("/auth/disable2fa", map[string]string{"password": "wrongpassword"}, true, 403)
	expect("/auth/disable2fa", map[string]string{"password": "password123"}, true, 200)
	if err = sessions.ValidateAccess(ctx, initial.SessionClaims); err != nil {
		t.Fatal("existing session revoked")
	}
	var enabled bool
	var step int64
	if err = pool.QueryRow(ctx, `SELECT totp_enabled, totp_secret, totp_last_step FROM users WHERE id=$1`, u.ID).Scan(&enabled, &stored, &step); err != nil {
		t.Fatal(err)
	}
	if enabled || stored != nil || step != -1 {
		t.Fatal("disable did not delete secret")
	}
	replacement, err := uc.EnableTwoFactor(ctx, u.ID)
	if err != nil || replacement.Secret == setup.Secret {
		t.Fatal("secret reused after disable")
	}
	// Исчерпание лимита привязано к аккаунту и не сбрасывается другим запросом настройки.
	for range 5 {
		_ = uc.ConfirmTwoFactor(ctx, u.ID, invalid)
	}
	if _, err = uc.EnableTwoFactor(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	expect("/auth/confirm2fa", map[string]string{"user_code": invalid}, true, 429)
	for _, sensitive := range []string{setup.Secret, replacement.Secret, payload.QRCode, code, "password123", "otpauth://"} {
		if strings.Contains(logs.String(), sensitive) {
			t.Fatal("authentication secret found in logs")
		}
	}
	// Ошибка под блокировкой строки не должна сохранить частичную активацию.
	rollback := errors.New("test rollback")
	err = userdb.NewUserRepository(pool).WithAuthLock(ctx, u.ID, func(state *usecase.AuthState) error { state.User.Has2FA = true; state.LastStep = 123; return rollback })
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	current, err = uc.CurrentUser(ctx, u.ID)
	if err != nil || current.Has2FA {
		t.Fatal("rollback activated 2FA")
	}
}
