package middleware

import (
	"crypto/hmac"
	"net/http"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/auth"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/httperr"
)

// CSRF защищает весь API до разбора параметров сгенерированным роутером.
func CSRF(tokens TokenParser, csrf *auth.CSRF, prefix string, allowedOrigins []string) func(http.Handler) http.Handler {
	origins := make(map[string]bool, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		origins[origin] = true
	}
	return func(next http.Handler) http.Handler {
		authenticated := Authenticate(tokens)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			exempt := path == prefix+"/auth/login" || path == prefix+"/auth/register" || path == prefix+"/auth/refresh"
			if userID, ok := auth.UserIDFromContext(r.Context()); changingMethod(r.Method) && ok && !exempt {
				if !csrf.Valid(r.Header.Get(auth.CSRFHeaderName), userID) {
					csrfInvalid(w)
					return
				}
			}
			next.ServeHTTP(w, r)
		}))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if (r.URL.Path != prefix && !strings.HasPrefix(r.URL.Path, prefix+"/")) || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			cookies := r.CookiesNamed(auth.CSRFCookieName)
			if len(cookies) == 0 {
				http.SetCookie(w, csrf.Cookie(csrf.Anonymous()))
			}
			if changingMethod(r.Method) {
				path := r.URL.Path
				if path == prefix+"/auth/login" || path == prefix+"/auth/register" || path == prefix+"/auth/refresh" {
					if values := r.Header.Values("Origin"); len(values) != 0 && (len(values) != 1 || !origins[values[0]]) {
						csrfInvalid(w)
						return
					}
				}
				values := r.Header.Values(auth.CSRFHeaderName)
				if len(cookies) != 1 || len(values) != 1 || values[0] == "" || cookies[0].Value == "" ||
					!hmac.Equal([]byte(values[0]), []byte(cookies[0].Value)) {
					csrfInvalid(w)
					return
				}
			}
			authenticated.ServeHTTP(w, r)
		})
	}
}

func changingMethod(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func csrfInvalid(w http.ResponseWriter) {
	httperr.Write(w, http.StatusForbidden, api.CsrfInvalid, "CSRF token is invalid")
}
