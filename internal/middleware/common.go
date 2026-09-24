package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/httperr"
	"github.com/google/uuid"
)

type requestIDKey struct{}

// RequestID takes X-Request-ID from the request or generates one.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = uuid.New().String()
		}
		w.Header().Set("X-Request-ID", reqID)

		ctx := context.WithValue(r.Context(), requestIDKey{}, reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Logging logs every processed request.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID, _ := r.Context().Value(requestIDKey{}).(string)

		next.ServeHTTP(w, r)

		slog.Info("request processed",
			slog.String("request_id", reqID),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Duration("duration", time.Since(start)),
		)
	})
}

// Recover turns a panic into 500 in the Error format.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				httperr.Internal(w, r, fmt.Errorf("panic: %v", rec))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
