// Package auth выпускает и проверяет access-токены и передаёт пользователя
// через контекст запроса.
package auth

import "context"

type userIDKey struct{}

func WithUserID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, userIDKey{}, id)
}

func UserIDFromContext(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(userIDKey{}).(int64)
	return id, ok
}
