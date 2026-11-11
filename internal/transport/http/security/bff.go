package security

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/httperr"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/principal"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
)

// S2S проверяет вызывающий сервис до любой аутентификации пользователя и разбора тела.
// Проверяются только API-маршруты, внутренний GET /health доступен без неё.
func S2S(key, prefix string) func(http.Handler) http.Handler {
	allowed := sha256.Sum256([]byte(key))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != prefix && !strings.HasPrefix(r.URL.Path, prefix+"/") {
				next.ServeHTTP(w, r)
				return
			}
			values := r.Header.Values("X-BFF-Key")
			matched := 0
			if len(values) == 1 && values[0] != "" {
				hash := sha256.Sum256([]byte(values[0]))
				matched = subtle.ConstantTimeCompare(hash[:], allowed[:])
			}
			if matched != 1 {
				httperr.Write(w, http.StatusForbidden, api.ErrorCodeS2sForbidden, "service access forbidden")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// AuthenticateBearer никогда не смотрит в cookies браузера. Validator решает, какие
// операции требуют аутентифицированного субъекта.
type AccessSessions interface {
	ValidateAccess(context.Context, usecase.SessionClaims) error
}

func AuthenticateBearer(tokens TokenParser, sessions AccessSessions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/health" {
				next.ServeHTTP(w, r)
				return
			}
			values := r.Header.Values("Authorization")
			if len(values) == 1 {
				scheme, token, ok := strings.Cut(values[0], " ")
				if ok && strings.EqualFold(scheme, "Bearer") && token != "" && !strings.ContainsAny(token, " \t\r\n") {
					if claims, err := tokens.Parse(token); err == nil {
						if err = sessions.ValidateAccess(r.Context(), claims); err != nil {
							if errors.Is(err, usecase.ErrInvalidSession) {
								httperr.Write(w, http.StatusUnauthorized, api.ErrorCodeUnauthorized, "authentication required")
							} else {
								httperr.ResponseError(w, r, err)
							}
							return
						}
						r = r.WithContext(principal.WithSession(r.Context(), claims))
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
