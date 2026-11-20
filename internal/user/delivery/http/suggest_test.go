package delivery

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"go.uber.org/mock/gomock"
)

func TestSuggestUsersHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		auth       bool
		err        error
		status     int
	}{
		{"success", "?q=Al", true, nil, 200},
		{"anonymous", "?q=Al", false, nil, 401},
		{"invalid", "?q=%25", true, usecase.ErrValidation, 400},
		{"limited", "?q=Al", true, usecase.ErrRateLimited, 429},
		{"limit zero", "?q=Al&limit=0", true, nil, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uc := NewMockUsecase(gomock.NewController(t))
			if tc.auth && tc.name != "limit zero" {
				q := "Al"
				if tc.name == "invalid" {
					q = "%"
				}
				uc.EXPECT().SuggestUsers(gomock.Any(), int64(42), q, 10).Return([]domain.UserSuggestion{{ID: 2, Login: "Alice"}}, tc.err)
			}
			router := newRouter(t, uc)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/users/suggest"+tc.path, nil)
			req.Header.Set("X-BFF-Key", "test-bff-key")
			if tc.auth {
				req.Header.Set("Authorization", "Bearer access-42")
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if tc.status == 200 && (!strings.Contains(rec.Body.String(), `"login":"Alice"`) || strings.Contains(rec.Body.String(), "password")) {
				t.Fatal(rec.Body.String())
			}
		})
	}
}
