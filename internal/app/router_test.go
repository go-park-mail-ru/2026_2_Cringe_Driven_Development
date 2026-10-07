package app

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestRouterRequestIDOnEveryResponse(t *testing.T) {
	handler, csrf, _, access, _ := csrfRouter(t)
	for _, tt := range []struct {
		name, method, path, token, access, origin string
		status                                    int
	}{
		{name: "health", method: "GET", path: "/health", status: 200},
		{name: "success", method: "GET", path: baseURL + "/users/me", access: access, status: 200},
		{name: "unauthorized", method: "GET", path: baseURL + "/users/me", status: 401},
		{name: "csrf copies", method: "POST", path: baseURL + "/auth/login", status: 403},
		{name: "csrf signature", method: "POST", path: baseURL + "/notebooks", token: csrf.Signed(2), access: access, status: 403},
		{name: "csrf origin", method: "POST", path: baseURL + "/auth/login", token: csrf.Anonymous(), origin: "https://evil.example", status: 403},
		{name: "preflight", method: "OPTIONS", path: baseURL + "/users/me", origin: "https://cellestial.ru", status: 204},
		{name: "not found", method: "GET", path: "/missing", status: 404},
		{name: "method not allowed", method: "POST", path: "/health", status: 405},
		{name: "invalid parameter", method: "GET", path: baseURL + "/notebooks/invalid", status: 400},
		// Тестовый usecase не реализует List: panic должен превратиться в ответ 500 с request ID.
		{name: "recovered panic", method: "GET", path: baseURL + "/notebooks", access: access, status: 500},
	} {
		for _, requestID := range []string{"", "client-request-id"} {
			t.Run(tt.name+"/"+requestID, func(t *testing.T) {
				req := csrfRequest(tt.method, tt.path, tt.token, tt.access, "")
				if requestID != "" {
					req.Header.Set("X-Request-ID", requestID)
				}
				if tt.origin != "" {
					req.Header.Set("Origin", tt.origin)
				}
				if tt.method == http.MethodOptions {
					req.Header.Set("Access-Control-Request-Method", "POST")
				}
				rec := serveCSRF(handler, req)
				if rec.Code != tt.status {
					t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body)
				}
				got := rec.Header().Get("X-Request-ID")
				if requestID != "" {
					if got != requestID {
						t.Fatalf("request ID = %q, want %q", got, requestID)
					}
				} else if _, err := uuid.Parse(got); err != nil {
					t.Fatalf("missing or invalid generated request ID: %q", got)
				}
				if tt.name == "preflight" && rec.Body.Len() != 0 {
					t.Fatal("preflight must have no body")
				}
			})
		}
	}
}
