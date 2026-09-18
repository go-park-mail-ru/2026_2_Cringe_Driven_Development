package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5/pgxpool"
)

type contextKey string

const requestIDKey contextKey = "requestID"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	if err := run(); err != nil {
		slog.Error("application stopped with error", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	port := getEnv("PORT", "8080")
	writeTimeout := getEnvDuration("WRITE_TIMEOUT", 15*time.Second)
	readTimeout := getEnvDuration("READ_TIMEOUT", 15*time.Second)

	dbUser := getEnv("POSTGRES_USER", "postgres")
	dbPass := getEnv("POSTGRES_PASSWORD", "postgres")
	dbHost := getEnv("POSTGRES_HOST", "db")
	dbName := getEnv("POSTGRES_DB", "app_db")

	ctx := context.Background()
	dbURL := fmt.Sprintf("postgres://%s:%s@%s:5432/%s", dbUser, dbPass, dbHost, dbName)

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("unable to connect to database: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("database ping failed: %w", err)
	}
	slog.Info("connected to postgres successfully")

	r := mux.NewRouter()

	r.Use(recoverMiddleware)
	r.Use(requestIDMiddleware)
	r.Use(loggingMiddleware)

	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Явно показываем линтеру и другим разработчикам, что игнорируем ошибку
		_, _ = w.Write([]byte(`{"status": "alive"}`))
	}).Methods(http.MethodGet)

	srv := &http.Server{
		Handler:      r,
		Addr:         ":" + port,
		WriteTimeout: writeTimeout,
		ReadTimeout:  readTimeout,
	}

	slog.Info("starting server",
		slog.String("port", port),
		slog.String("write_timeout", writeTimeout.String()),
		slog.String("read_timeout", readTimeout.String()),
	)

	if err := srv.ListenAndServe(); err != nil {
		return fmt.Errorf("server startup failed: %w", err)
	}

	return nil
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if value, exists := os.LookupEnv(key); exists {
		if d, err := time.ParseDuration(value); err == nil {
			return d
		}
		slog.Warn("invalid duration in env, using fallback", slog.String("key", key))
	}
	return fallback
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = uuid.New().String()
		}
		w.Header().Set("X-Request-ID", reqID)

		ctx := context.WithValue(r.Context(), requestIDKey, reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID, _ := r.Context().Value(requestIDKey).(string)

		next.ServeHTTP(w, r)

		slog.Info("request processed",
			slog.String("request_id", reqID),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Duration("duration", time.Since(start)),
		)
	})
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("panic recovered", slog.Any("err", err))
				http.Error(w, `{"error": "Internal Server Error"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
