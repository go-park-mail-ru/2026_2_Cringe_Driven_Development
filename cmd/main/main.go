package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/auth"
	notebookdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery"
	notebookpostgres "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/repository/postgres"
	notebooks3 "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/repository/s3"
	notebookusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	userdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/delivery"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/repository/postgres"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
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
	corsOrigins, err := parseCORSOrigins(getEnv("CORS_ALLOWED_ORIGINS", defaultCORSOrigins))
	if err != nil {
		return err
	}
	notebooksBucket := os.Getenv("S3_NOTEBOOKS_BUCKET")
	if notebooksBucket == "" {
		return errors.New("S3_NOTEBOOKS_BUCKET is not set")
	}

	dbUser := getEnv("POSTGRES_USER", "postgres")
	dbPass := getEnv("POSTGRES_PASSWORD", "postgres")
	dbHost := getEnv("POSTGRES_HOST", "db")
	dbName := getEnv("POSTGRES_DB", "app_db")

	ctx := context.Background()
	dbURL := postgresURL(dbUser, dbPass, dbHost, dbName)

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

	tokens := auth.NewAccessToken([]byte(jwtSecret), accessTTL)
	userRepo := postgres.NewUserRepository(pool)
	userUsecase := usecase.NewUserUsecase(userRepo, tokens, refreshTTL)

	s3Client, err := notebooks3.NewClient(ctx)
	if err != nil {
		return err
	}
	notebookUsecase := notebookusecase.NewNotebookUsecase(
		notebookpostgres.NewNotebookRepository(pool),
		notebooks3.NewFileStorage(s3Client, notebooksBucket),
	)
	srv := server{
		userHandler: userdelivery.NewHandler(userUsecase,
			userdelivery.CookieConfig{Path: baseURL + "/auth", Secure: cookieSecure}),
		notebookHandler: notebookdelivery.NewHandler(notebookUsecase),
	}
	router, err := newRouter(srv, tokens, corsOrigins)
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

func postgresURL(user, password, host, database string) string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   host + ":5432",
		Path:   "/" + database,
	}
	return u.String()
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
