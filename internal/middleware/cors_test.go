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
		{name: "чужой origin", allowed: []string{front}, method: http.MethodGet, origin: "http://evil.example",
			wantStatus: http.StatusOK, wantReached: true},
		{name: "localhost и 127.0.0.1 — разные origin", allowed: []string{front}, method: http.MethodGet,
			origin: "http://127.0.0.1:5173", wantStatus: http.StatusOK, wantReached: true},
		{name: "предварительный запрос", allowed: []string{front}, method: http.MethodOptions, origin: front,
			preflight: true, wantStatus: http.StatusNoContent, wantOrigin: front},
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
