// Package principal передаёт пользователя через контекст HTTP-запроса.
package principal

import (
	"context"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
)

// userIDKey отделяет проверенный ID пользователя от других полей контекста.
type userIDKey struct{}

// WithUserID связывает проверенный ID пользователя с контекстом ctx.
func WithUserID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, userIDKey{}, id)
}

// UserIDFromContext извлекает проверенный ID пользователя из контекста ctx.
func UserIDFromContext(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(userIDKey{}).(int64)
	return id, ok
}

// sessionKey отделяет проверенные данные сессии от других полей контекста.
type sessionKey struct{}

// WithSession связывает данные проверенной сессии с контекстом ctx.
func WithSession(ctx context.Context, claims usecase.SessionClaims) context.Context {
	return context.WithValue(WithUserID(ctx, claims.UserID), sessionKey{}, claims)
}

// SessionFromContext извлекает данные проверенной сессии из контекста ctx.
func SessionFromContext(ctx context.Context) (usecase.SessionClaims, bool) {
	claims, ok := ctx.Value(sessionKey{}).(usecase.SessionClaims)
	return claims, ok
}
