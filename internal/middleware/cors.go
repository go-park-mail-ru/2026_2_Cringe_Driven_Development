package middleware

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

// CORS разрешает запросы со страниц из allowed; пустой список выключает его.
func CORS(allowed []string) func(http.Handler) http.Handler {
	origins := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		origins[o] = true
	}
	return func(next http.Handler) http.Handler {
		if len(origins) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Add("Vary", "Origin")
			origin := r.Header.Get("Origin")
			if !origins[origin] {
				next.ServeHTTP(w, r)
				return
			}
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Expose-Headers", "X-Request-Id")

			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID, X-CSRF-Token")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// DefaultCORSOrigins содержит origin фронта, разрешённые без настройки окружения.
const DefaultCORSOrigins = "https://cellestial.ru,http://localhost:5173"

// ParseCORSOrigins разбирает и проверяет белый список; пустая строка выключает CORS.
func ParseCORSOrigins(value string) ([]string, error) {
	origins := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
	for _, origin := range origins {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
			u.Hostname() == "" || strings.Contains(u.Host, "*") || u.User != nil ||
			u.Path != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(origin, "#") {
			return nil, fmt.Errorf("CORS_ALLOWED_ORIGINS: invalid origin %q; expected an HTTP(S) origin without credentials, path, query or fragment", origin)
		}
	}
	return origins, nil
}
