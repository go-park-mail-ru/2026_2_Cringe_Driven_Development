// Package middleware содержит HTTP middleware сервера.
package middleware

import (
	"net/http"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/auth"
)

type TokenParser interface {
	Parse(token string) (int64, error)
}

// Authenticate кладёт пользователя в контекст, если в запросе валидный токен.
// Сам запросы не отклоняет: закрытые ручки по контракту закрывает Validator.
func Authenticate(tokens TokenParser) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie("access_token")
			if err == nil && cookie.Value != "" {
				if userID, err := tokens.Parse(cookie.Value); err == nil {
					r = r.WithContext(auth.WithUserID(r.Context(), userID))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
