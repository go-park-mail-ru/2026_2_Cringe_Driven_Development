package httptransport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	usersecurity "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	userdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/delivery/http"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
)

// passwordUsers считает вызовы usecase: невалидный пароль не должен дойти до хеширования.
type passwordUsers struct {
	usecase.Usecase
	calls int
}

func (u *passwordUsers) Register(context.Context, string, string) (domain.User, usecase.Session, error) {
	u.calls++
	return domain.User{}, usecase.Session{}, usecase.ErrLoginTaken
}
func (u *passwordUsers) Login(context.Context, string, string, ...*string) (domain.User, usecase.Session, error) {
	u.calls++
	return domain.User{}, usecase.Session{}, usecase.ErrInvalidCredentials
}
func (u *passwordUsers) ChangeCurrentUserPassword(context.Context, int64, string, string) error {
	u.calls++
	return usecase.ErrInvalidCurrentPassword
}
func (u *passwordUsers) DisableTwoFactor(context.Context, int64, string) error {
	u.calls++
	return usecase.ErrInvalidCurrentPassword
}

func TestPasswordSchemaValidation(t *testing.T) {
	bodies := map[string]func(string) any{
		"/auth/register":     func(p string) any { return map[string]string{"login": "alice", "password": p} },
		"/auth/login":        func(p string) any { return map[string]string{"login": "alice", "password": p} },
		"/auth/disable2fa":   func(p string) any { return map[string]string{"password": p} },
		"/users/me/password": func(p string) any { return map[string]string{"current_password": p, "new_password": "valid-password"} },
		"/users/me/password#new": func(p string) any {
			return map[string]string{"current_password": "valid-password", "new_password": p}
		},
	}
	invalid := []string{strings.Repeat("a", 7), strings.Repeat("a", 73), "pass word1", "password\n1", "парольпароль", "password😀"}
	valid := []string{"a<&>\"'!~", strings.Repeat("Z", 72)}
	for path, body := range bodies {
		for _, p := range append(invalid, valid...) {
			ok := p == valid[0] || p == valid[1]
			t.Run(path+"/"+p, func(t *testing.T) {
				users := &passwordUsers{}
				tokens := usersecurity.NewAccessToken([]byte("password-test"), time.Minute)
				access, err := tokens.Issue(testClaims(42))
				if err != nil {
					t.Fatal(err)
				}
				h, err := NewRouter(Server{UserHandler: userdelivery.NewHandler(users)}, tokens, allowSessions{}, "test-key")
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(body(p))
				method := http.MethodPost
				if strings.HasPrefix(path, "/users/me/password") {
					method = http.MethodPut
				}
				req := httptest.NewRequest(method, BaseURL+strings.TrimSuffix(path, "#new"), strings.NewReader(string(raw)))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-BFF-Key", "test-key")
				req.Header.Set("Authorization", "Bearer "+access)
				res := serveRouter(h, req)
				var e struct{ Code string }
				_ = json.Unmarshal(res.Body.Bytes(), &e)
				if ok {
					if users.calls != 1 || e.Code == "validation_error" {
						t.Fatalf("valid password rejected: calls=%d status=%d body=%s", users.calls, res.Code, res.Body)
					}
					return
				}
				if users.calls != 0 || res.Code != http.StatusBadRequest || e.Code != "validation_error" {
					t.Fatalf("calls=%d status=%d body=%s", users.calls, res.Code, res.Body)
				}
				if strings.Contains(res.Body.String(), p) {
					t.Fatalf("password leaked: %s", res.Body)
				}
			})
		}
	}
}
