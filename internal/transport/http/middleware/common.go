package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/observability"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/httperr"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// requestMetaKey хранит уникальный ключ метаданных access log.
type requestMetaKey struct{}

// requestMeta хранит шаблон маршрута для сводки запроса.
type requestMeta struct{ route string }

// validRequestID разрешает ограниченный набор символов для корреляции логов.
func validRequestID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// RequestID принимает безопасный X-Request-ID или создаёт новый и передаёт его в next.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if !validRequestID(reqID) {
			reqID = uuid.New().String()
		}
		w.Header().Set("X-Request-ID", reqID)

		ctx := observability.WithRequestID(r.Context(), reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Logging записывает метод, шаблон маршрута, статус и длительность вызова next.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		meta := &requestMeta{route: "unmatched"}
		ctx := context.WithValue(r.Context(), requestMetaKey{}, meta)
		writer := newResponseWriter(w)
		next.ServeHTTP(writer.withFlush(), r.WithContext(ctx))
		status := writer.status
		if status == 0 {
			status = http.StatusOK
		}
		slog.InfoContext(ctx, "http.request",
			slog.String("method", r.Method),
			slog.String("route", meta.route),
			slog.Int("status", status),
			slog.Duration("duration", time.Since(start)),
		)
	})
}

// RecordRoute сохраняет шаблон сопоставленного маршрута для access log.
func RecordRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if meta, ok := r.Context().Value(requestMetaKey{}).(*requestMeta); ok {
			if route := mux.CurrentRoute(r); route != nil {
				if template, err := route.GetPathTemplate(); err == nil {
					meta.route = template
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Recover преобразует panic из next во внутреннюю HTTP-ошибку.
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
