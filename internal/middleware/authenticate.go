// Package middleware contains the HTTP middleware of the API server.
package middleware

import (
	"net/http"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/auth"
	"github.com/google/uuid"
)

// TokenParser verifies an access token and returns its user.
type TokenParser interface {
	Parse(token string) (uuid.UUID, error)
}

// Authenticate puts the user into the context when the request carries a
// valid access token. It never rejects a request: endpoints that require a
// user are closed by the validator according to the contract.
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
