package middleware

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/httperr"
	"github.com/gorilla/mux"
)

func TestCORS(t *testing.T) {
	const front = "http://localhost:5173"
	tests := []struct {
		name        string
		allowed     []string
		method      string
		origin      string
		preflight   bool
		wantStatus  int
		wantOrigin  string
		wantReached bool
	}{
		{name: "CORS выключен", allowed: nil, method: http.MethodGet, origin: front,
			wantStatus: http.StatusOK, wantReached: true},
		{name: "разрешённый origin", allowed: []string{front}, method: http.MethodGet, origin: front,
			wantStatus: http.StatusOK, wantOrigin: front, wantReached: true},
		{name: "production origin", allowed: []string{front, "https://cellestial.ru"}, method: http.MethodGet,
			origin: "https://cellestial.ru", wantStatus: http.StatusOK, wantOrigin: "https://cellestial.ru", wantReached: true},
		{name: "без origin", allowed: []string{front}, method: http.MethodGet,
			wantStatus: http.StatusOK, wantReached: true},
		{name: "чужой origin", allowed: []string{front}, method: http.MethodGet, origin: "http://evil.example",
			wantStatus: http.StatusOK, wantReached: true},
		{name: "localhost и 127.0.0.1 — разные origin", allowed: []string{front}, method: http.MethodGet,
			origin: "http://127.0.0.1:5173", wantStatus: http.StatusOK, wantReached: true},
		{name: "предварительный запрос", allowed: []string{front}, method: http.MethodOptions, origin: front,
			preflight: true, wantStatus: http.StatusNoContent, wantOrigin: front},
		{name: "чужой preflight", allowed: []string{front}, method: http.MethodOptions, origin: "https://evil.example",
			preflight: true, wantStatus: http.StatusOK, wantReached: true},
		{name: "обычный OPTIONS", allowed: []string{front}, method: http.MethodOptions, origin: front,
			wantStatus: http.StatusOK, wantOrigin: front, wantReached: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
			req := httptest.NewRequest(tt.method, "/api/v1/notebooks", nil)
			req.Header.Set("Origin", tt.origin)
			if tt.preflight {
				req.Header.Set("Access-Control-Request-Method", http.MethodPost)
			}
			rec := httptest.NewRecorder()

			CORS(tt.allowed)(next).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tt.wantOrigin {
				t.Errorf("Allow-Origin = %q, want %q", got, tt.wantOrigin)
			}
			if reached != tt.wantReached {
				t.Errorf("запрос дошёл до хендлера: %v, want %v", reached, tt.wantReached)
			}
			if tt.wantOrigin == "" {
				for _, header := range []string{"Access-Control-Allow-Credentials", "Access-Control-Expose-Headers", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers"} {
					if got := rec.Header().Get(header); got != "" {
						t.Errorf("%s = %q, want empty", header, got)
					}
				}
			}
			wantVary := "Origin"
			if len(tt.allowed) == 0 {
				wantVary = ""
			}
			if got := rec.Header().Get("Vary"); got != wantVary {
				t.Errorf("Vary = %q, want %q", got, wantVary)
			}
			if tt.wantOrigin == "" {
				return
			}
			for header, want := range map[string]string{
				"Access-Control-Allow-Credentials": "true",
				"Access-Control-Expose-Headers":    "Authorization, X-Request-Id",
			} {
				if got := rec.Header().Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
		})
	}
}

func TestCORSPreflight(t *testing.T) {
	const origin = "http://localhost:5173"
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		for _, header := range []string{"Authorization", "Content-Type", "X-Request-ID"} {
			t.Run(method+"/"+header, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodOptions, "/api/v1/notebooks", nil)
				req.Header.Set("Origin", origin)
				req.Header.Set("Access-Control-Request-Method", method)
				req.Header.Set("Access-Control-Request-Headers", header)
				rec := httptest.NewRecorder()
				CORS([]string{origin})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					t.Fatal("preflight reached handler")
				})).ServeHTTP(rec, req)
				if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
					t.Fatalf("preflight status = %d, body = %q", rec.Code, rec.Body.String())
				}
				for name, want := range map[string]string{
					"Access-Control-Allow-Methods": "GET, POST, DELETE",
					"Access-Control-Allow-Headers": "Authorization, Content-Type, X-Request-ID",
					"Access-Control-Max-Age":       "600",
				} {
					if got := rec.Header().Get(name); got != want {
						t.Errorf("%s = %q, want %q", name, got, want)
					}
				}
			})
		}
	}
}

func TestCORSPreservesVary(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Add("Vary", "Accept-Encoding")
	CORS([]string{"https://cellestial.ru"})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, "/", nil))
	values := rec.Header().Values("Vary")
	if len(values) != 2 || values[0] != "Accept-Encoding" || values[1] != "Origin" {
		t.Errorf("Vary = %v, want [Accept-Encoding Origin]", values)
	}
}

func TestCORSOriginsConfig(t *testing.T) {
	for _, tt := range []struct {
		name, value string
		want        []string
	}{
		{name: "default", value: DefaultCORSOrigins, want: []string{"https://cellestial.ru", "http://localhost:5173"}},
		{name: "disabled", value: ""},
		{name: "override", value: "https://dev.example", want: []string{"https://dev.example"}},
		{name: "separators", value: " ,https://cellestial.ru,\t http://localhost:5173\n", want: []string{"https://cellestial.ru", "http://localhost:5173"}},
		{name: "IPv6", value: "http://[::1]:5173", want: []string{"http://[::1]:5173"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCORSOrigins(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("origins = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCORSInvalidOrigins(t *testing.T) {
	for _, value := range []string{
		"*", "null", "https://*.example", "cellestial.ru", "ftp://example.com", "https://",
		"https://example.com/", "https://example.com/path", "https://example.com?",
		"https://example.com?x=1", "https://example.com#", "https://example.com#fragment",
		"https://user:password@example.com", "http://localhost:bad", "http://[invalid",
		"https://cellestial.ru,*",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := ParseCORSOrigins(value); err == nil {
				t.Error("invalid origin accepted")
			}
		})
	}
}

func TestCORSWithAPIMiddleware(t *testing.T) {
	const origin = "http://localhost:5173"
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	r := mux.NewRouter()
	r.NotFoundHandler = http.HandlerFunc(httperr.NotFound)
	r.MethodNotAllowedHandler = http.HandlerFunc(httperr.MethodNotAllowed)
	// Эти запросы завершаются до вызова API-хендлеров.
	api.HandlerWithOptions(nil, api.GorillaServerOptions{
		BaseURL:          "/api/v1",
		BaseRouter:       r,
		Middlewares:      []api.MiddlewareFunc{Validator(spec, "/api/v1"), Authenticate(nil)},
		ErrorHandlerFunc: httperr.RequestError,
	})
	router := CORS([]string{origin})(r)
	for _, tt := range []struct {
		name, method, path string
		status             int
	}{
		{"protected preflight", http.MethodOptions, "/api/v1/users/me", http.StatusNoContent},
		{"unauthorized", http.MethodGet, "/api/v1/users/me", http.StatusUnauthorized},
		{"not found", http.MethodGet, "/api/v1/missing", http.StatusNotFound},
		{"method not allowed", http.MethodPut, "/api/v1/users/me", http.StatusMethodNotAllowed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Origin", origin)
			if tt.method == http.MethodOptions {
				req.Header.Set("Access-Control-Request-Method", http.MethodGet)
				req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type, X-Request-ID")
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body.String())
			}
			for header, want := range map[string]string{
				"Access-Control-Allow-Origin":      origin,
				"Access-Control-Allow-Credentials": "true",
				"Vary":                             "Origin",
			} {
				if got := rec.Header().Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
			if tt.method == http.MethodOptions {
				if rec.Body.Len() != 0 {
					t.Error("preflight has a response body")
				}
				if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "X-Request-ID") {
					t.Error("preflight does not allow X-Request-ID")
				}
			}
		})
	}
}
