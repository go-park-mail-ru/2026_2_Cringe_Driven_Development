package httptransport

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"go.uber.org/mock/gomock"
)

// Невалидный пароль отклоняет Validator: без EXPECT gomock падает на любом вызове usecase,
// то есть до хеширования и изменения состояния.
func TestPasswordSchemaValidation(t *testing.T) {
	f := newRouterFixture(t)
	anyArg := gomock.Any()
	routes := []struct {
		method, path string
		body         func(string) map[string]string
		expect       func(string)
	}{
		{http.MethodPost, "/auth/register",
			func(p string) map[string]string { return map[string]string{"login": "alice", "password": p} },
			func(p string) {
				f.users.EXPECT().Register(anyArg, "alice", p).Return(domain.User{}, usecase.Session{}, usecase.ErrLoginTaken)
			}},
		{http.MethodPost, "/auth/login",
			func(p string) map[string]string { return map[string]string{"login": "alice", "password": p} },
			func(p string) {
				f.users.EXPECT().Login(anyArg, "alice", p).Return(domain.User{}, usecase.Session{}, usecase.ErrInvalidCredentials)
			}},
		{http.MethodPost, "/auth/disable2fa",
			func(p string) map[string]string { return map[string]string{"password": p} },
			func(p string) {
				f.users.EXPECT().DisableTwoFactor(anyArg, anyArg, p).Return(usecase.ErrInvalidCurrentPassword)
			}},
		{http.MethodPut, "/users/me/password",
			func(p string) map[string]string {
				return map[string]string{"current_password": p, "new_password": "valid-password"}
			},
			func(p string) {
				f.users.EXPECT().ChangeCurrentUserPassword(anyArg, anyArg, p, "valid-password").Return(usecase.ErrInvalidCurrentPassword)
			}},
		{http.MethodPut, "/users/me/password",
			func(p string) map[string]string {
				return map[string]string{"current_password": "valid-password", "new_password": p}
			},
			func(p string) {
				f.users.EXPECT().ChangeCurrentUserPassword(anyArg, anyArg, "valid-password", p).Return(usecase.ErrInvalidCurrentPassword)
			}},
	}
	cases := []struct {
		password string
		valid    bool
	}{
		{strings.Repeat("a", 7), false}, {strings.Repeat("a", 73), false}, {"pass word1", false},
		{"password\n1", false}, {"парольпароль", false}, {"password😀", false},
		{`a<&>"'!~`, true}, {strings.Repeat("Z", 72), true},
	}
	for _, r := range routes {
		for _, c := range cases {
			if c.valid {
				r.expect(c.password)
			}
			raw, _ := json.Marshal(r.body(c.password))
			req := routerRequest(r.method, BaseURL+r.path, "test-key", f.access)
			req.Body, req.ContentLength = io.NopCloser(bytes.NewReader(raw)), int64(len(raw))
			res := serveRouter(f.handler, req)
			var e struct{ Code string }
			_ = json.Unmarshal(res.Body.Bytes(), &e)
			switch {
			case c.valid && e.Code == "validation_error":
				t.Errorf("%s %q: valid password rejected: %s", r.path, c.password, res.Body)
			case !c.valid && (res.Code != http.StatusBadRequest || e.Code != "validation_error"):
				t.Errorf("%s %q: status=%d body=%s", r.path, c.password, res.Code, res.Body)
			case !c.valid && strings.Contains(res.Body.String(), c.password):
				t.Errorf("%s: password leaked: %s", r.path, res.Body)
			}
		}
	}
}
