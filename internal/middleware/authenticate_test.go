package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/auth"
)

func TestAuthenticateCookies(t *testing.T) {
	tokens := auth.NewAccessToken([]byte("test-secret"), time.Minute)
	valid, err := tokens.Issue(42)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := auth.NewAccessToken([]byte("test-secret"), -time.Minute).Issue(42)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	type contextKey struct{}
	for _, tt := range []struct {
		name, cookie, header   string
		present, authenticated bool
	}{
		{name: "missing"},
		{name: "empty", present: true},
		{name: "invalid", present: true, cookie: "invalid"},
		{name: "expired", present: true, cookie: expired},
		{name: "bearer only", header: "Bearer " + valid},
		{name: "valid", present: true, cookie: valid, authenticated: true},
		{name: "cookie wins", present: true, cookie: valid, header: "Bearer invalid", authenticated: true},
		{name: "no bearer fallback", present: true, cookie: "invalid", header: "Bearer " + valid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			specHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Context().Value(contextKey{}) != "preserved" {
					t.Error("request context lost")
				}
				id, ok := auth.UserIDFromContext(r.Context())
				if ok != tt.authenticated || (ok && id != 42) {
					t.Errorf("user = %d, %v", id, ok)
				}

				w.WriteHeader(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
			req = req.WithContext(context.WithValue(req.Context(), contextKey{}, "preserved"))
			if tt.present {
				req.AddCookie(&http.Cookie{Name: "access_token", Value: tt.cookie})
			}
			req.Header.Set("Authorization", tt.header)
			rec := httptest.NewRecorder()
			Authenticate(tokens)(Validator(spec, "/api/v1")(specHandler)).ServeHTTP(rec, req)
			wantStatus := http.StatusUnauthorized
			if tt.authenticated {
				wantStatus = http.StatusOK
			}
			if rec.Code != wantStatus {
				t.Errorf("status = %d, want %d: %s", rec.Code, wantStatus, rec.Body)
			}
		})
	}
}
