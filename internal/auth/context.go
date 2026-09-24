// Package auth issues and verifies access tokens and carries the
// authenticated user through the request context.
package auth

import (
	"context"

	"github.com/google/uuid"
)

type userIDKey struct{}

// WithUserID returns a copy of ctx that carries the authenticated user.
func WithUserID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, userIDKey{}, id)
}

// UserIDFromContext returns the authenticated user, if there is one.
func UserIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userIDKey{}).(uuid.UUID)
	return id, ok
}
