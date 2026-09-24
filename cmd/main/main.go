package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/auth"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/migrations"
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
	host := getEnv("HOST", "0.0.0.0")
	port := getEnv("PORT", "8080")
	writeTimeout := getEnvDuration("WRITE_TIMEOUT", 15*time.Second)
	readTimeout := getEnvDuration("READ_TIMEOUT", 15*time.Second)

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		return errors.New("JWT_SECRET is not set")
	}
	accessTTL := getEnvDuration("ACCESS_TOKEN_TTL", 15*time.Minute)

	refreshTTL := getEnvDuration("REFRESH_TOKEN_TTL", 720*time.Hour)
	cookieSecure := getEnv("COOKIE_SECURE", "false") == "true"

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

	if err = pool.Ping(ctx); err != nil {
		return fmt.Errorf("database ping failed: %w", err)
	}
	slog.Info("connected to postgres successfully")

	if err = migrations.Up(ctx, pool); err != nil {
		return err
	}

	tokens := auth.NewAccessTokens([]byte(jwtSecret), accessTTL)
	srv := server{
		userHandler: user.NewHandler(
			user.NewService(user.NewRepository(pool), tokens, refreshTTL),
			user.CookieConfig{Path: baseURL + "/auth", Secure: cookieSecure},
		),

		notebookHandler: notebook.NewHandler(),
	}
	router, err := newRouter(srv, tokens)
	if err != nil {
		return err
	}

	bindAddr := fmt.Sprintf("%s:%s", host, port)

	httpServer := &http.Server{
		Handler:      router,
		Addr:         bindAddr,
		WriteTimeout: writeTimeout,
		ReadTimeout:  readTimeout,
	}

	slog.Info("starting server",
		slog.String("addr", bindAddr),
		slog.String("write_timeout", writeTimeout.String()),
		slog.String("read_timeout", readTimeout.String()),
	)

	if err := httpServer.ListenAndServe(); err != nil {
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
