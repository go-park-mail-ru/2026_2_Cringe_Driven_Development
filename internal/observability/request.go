// Package observability предоставляет контекст запроса и безопасное структурированное логирование.
package observability

import "context"

// requestIDKey отделяет ключ контекста от ключей других пакетов.
type requestIDKey struct{}

// WithRequestID привязывает идентификатор id к контексту ctx.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID возвращает идентификатор запроса из ctx или пустую строку.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
