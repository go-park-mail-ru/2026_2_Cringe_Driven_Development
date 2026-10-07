package middleware

import (
	"context"
	"crypto/hmac"
	"errors"
	"net/http"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/auth"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/httperr"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
)

// RefreshUserResolver читает владельца действующей refresh-сессии без её изменения.
type RefreshUserResolver interface {
	RefreshUserID(context.Context, string) (int64, error)
}

// CSRF защищает весь API до разбора параметров сгенерированным роутером.
func CSRF(tokens TokenParser, csrf *auth.CSRF, prefix string, allowedOrigins []string, refreshUsers RefreshUserResolver, appOrigin string) func(http.Handler) http.Handler {
	origins := make(map[string]bool, len(allowedOrigins)+1)
	origins[appOrigin] = true
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
			// Восстанавливаем подпись для старых сессий, но проверяем исходный запрос.
			// Подписанные (в том числе испорченные) cookie автоматически не заменяем.
			if len(cookies) == 0 || (len(cookies) == 1 && auth.IsAnonymousCSRF(cookies[0].Value)) {
				origin := r.Header.Values("Origin")
				trusted := len(origin) == 0 || (len(origin) == 1 && origins[origin[0]])
				var userID int64
				if trusted {
					if access, err := r.Cookie("access_token"); err == nil {
						if id, parseErr := tokens.Parse(access.Value); parseErr == nil {
							userID = id
						}
					}
					if userID <= 0 && refreshUsers != nil {
						if refresh, err := r.Cookie("refresh_token"); err == nil && refresh.Value != "" {
							var lookupErr error
							userID, lookupErr = refreshUsers.RefreshUserID(r.Context(), refresh.Value)
							if lookupErr != nil {
								if !errors.Is(lookupErr, usecase.ErrInvalidRefresh) {
									if len(cookies) == 0 {
										http.SetCookie(w, csrf.Cookie(csrf.Anonymous()))
									}
									httperr.Internal(w, r, lookupErr)
									return
								}
								userID = 0
							}
						}
					}
				}
				if userID > 0 || len(cookies) == 0 {
					token := csrf.Anonymous()
					if userID > 0 {
						token = csrf.Signed(userID)
					}
					writer := &csrfBootstrapWriter{ResponseWriter: w, cookie: csrf.Cookie(token)}
					w = writer
					defer writer.ensureCookie()
				}
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

// csrfBootstrapWriter не дублирует CSRF-cookie, если обработчик сменил сессию.
type csrfBootstrapWriter struct {
	http.ResponseWriter
	cookie  *http.Cookie
	checked bool
}

func (w *csrfBootstrapWriter) ensureCookie() {
	if w.checked {
		return
	}
	w.checked = true
	w.Header().Set("Cache-Control", "no-store")
	response := &http.Response{Header: w.Header()}
	for _, cookie := range response.Cookies() {
		if cookie.Name == auth.CSRFCookieName {
			return
		}
	}
	http.SetCookie(w.ResponseWriter, w.cookie)
}

func (w *csrfBootstrapWriter) WriteHeader(status int) {
	w.ensureCookie()
	w.ResponseWriter.WriteHeader(status)
}

func (w *csrfBootstrapWriter) Write(body []byte) (int, error) {
	w.ensureCookie()
	return w.ResponseWriter.Write(body)
}

func (w *csrfBootstrapWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
