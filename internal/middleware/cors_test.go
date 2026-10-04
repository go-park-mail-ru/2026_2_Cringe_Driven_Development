package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
