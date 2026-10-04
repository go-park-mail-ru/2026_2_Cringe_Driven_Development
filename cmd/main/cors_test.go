package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestCORSOriginsConfig(t *testing.T) {
	tests := []struct {
		name    string
		value   *string
		want    []string
		wantErr bool
	}{
		{name: "unset", want: []string{"https://cellestial.ru", "http://localhost:5173"}},
		{name: "disabled", value: new("")},
		{name: "override", value: new("https://dev.example"), want: []string{"https://dev.example"}},
		{name: "separators", value: new(" ,https://cellestial.ru,\t http://localhost:5173\n"),
			want: []string{"https://cellestial.ru", "http://localhost:5173"}},
		{name: "IPv6", value: new("http://[::1]:5173"), want: []string{"http://[::1]:5173"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv registers restoration even when the test temporarily unsets it.
			t.Setenv("CORS_ALLOWED_ORIGINS", "")
			if tt.value == nil {
				if err := os.Unsetenv("CORS_ALLOWED_ORIGINS"); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv("CORS_ALLOWED_ORIGINS", *tt.value)
			}
			got, err := parseCORSOrigins(getEnv("CORS_ALLOWED_ORIGINS", defaultCORSOrigins))
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && !slices.Equal(got, tt.want) {
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
			if _, err := parseCORSOrigins(value); err == nil {
				t.Error("invalid origin accepted")
			}
		})
	}
}

func TestRouterCORS(t *testing.T) {
	const origin = "http://localhost:5173"
	router, err := newRouter(server{}, nil, []string{origin})
	if err != nil {
		t.Fatal(err)
	}
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
