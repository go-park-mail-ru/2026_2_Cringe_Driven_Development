// Package middleware содержит HTTP middleware сервера.
package middleware

import (
	"net/http"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/auth"
	"github.com/google/uuid"
)

type TokenParser interface {
	Parse(token string) (uuid.UUID, error)
}

// Authenticate кладёт пользователя в контекст, если в запросе валидный токен.
// Сам запросы не отклоняет: закрытые ручки по контракту закрывает Validator.
func Authenticate(tokens TokenParser) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if ok {
				if userID, err := tokens.Parse(token); err == nil {
					r = r.WithContext(auth.WithUserID(r.Context(), userID))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
