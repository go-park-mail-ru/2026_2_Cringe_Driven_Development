// Package app собирает зависимости, HTTP-роутер и запускает API.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/config"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/ipynb"
	notebookpostgres "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/postgres"
	notebooks3 "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/s3"
	notebookdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery/http"
	notebookusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	runtimemodule "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime"
	runtimeusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	sessionenv "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/adapters/environment"
	sessionpostgres "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/adapters/postgres"
	sessiondelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/delivery/http"
	sessionusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/usecase"
	httptransport "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http"
	avatarcodec "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/avatar"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/postgres"
	userredis "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/redis"
	avatars3 "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/s3"
	usersecurity "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	userdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/delivery/http"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// application — HTTP-сервер, provisioning и пользовательские runtime-сессии.
type application struct {
	// Server Общий HTTP-сервер приложения.
	Server *http.Server
	// Environments Сервис создания, чтения и удаления окружений.
	Environments runtimeusecase.EnvironmentService
	// RuntimeWorker nil, если RUNTIME_ENABLED=false.
	RuntimeWorker *runtimeusecase.Worker
	// SessionWorker Обработчик жизненного цикла пользовательских сессий.
	SessionWorker *sessionusecase.Worker
	// RuntimeMode Режим подключения сервиса окружений.
	RuntimeMode string
	// Close освобождает ресурс после завершения работы.
	Close func()
}

// newServer собирает приложение; вызывающий закрывает ресурсы после остановки.
func newServer(ctx context.Context, cfg config.Config) (*application, error) {
	dbURL := cfg.PostgresURL()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, startupError("database_connect", fmt.Errorf("unable to connect to database: %w", err))
	}
	client := redis.NewClient(&redis.Options{Addr: net.JoinHostPort(cfg.RedisHost, strconv.Itoa(cfg.RedisPort)), Password: cfg.RedisPassword, DB: cfg.RedisDB,
		DialTimeout: cfg.RedisDialTimeout, ReadTimeout: cfg.RedisOperationTimeout, WriteTimeout: cfg.RedisOperationTimeout, PoolTimeout: cfg.RedisOperationTimeout, ContextTimeoutEnabled: true, MaxRetries: -1})
	closeResources := func() { _ = client.Close(); pool.Close() }
	ready := false
	defer func() {
		if !ready {
			closeResources()
		}
	}()

	if err = pool.Ping(ctx); err != nil {
		return nil, startupError("database_connect", fmt.Errorf("database ping failed: %w", err))
	}
	slog.Info("connected to postgres successfully")

	if err = migrations.Up(ctx, pool); err != nil {
		return nil, startupError("database_migrate", err)
	}

	redisCtx, redisCancel := context.WithTimeout(ctx, cfg.RedisDialTimeout)
	err = client.Ping(redisCtx).Err()
	redisCancel()
	if err != nil {
		return nil, startupError("redis_connect", fmt.Errorf("redis ping: %w", err))
	}
	sessions := userredis.NewStore(client, "", cfg.RedisOperationTimeout)
	tokens := usersecurity.NewAccessToken([]byte(cfg.JWTSecret), cfg.AccessTTL)
	userRepo := postgres.NewUserRepository(pool)
	userUsecase := usecase.NewUserUsecase(userRepo, sessions, tokens, usersecurity.PasswordHasher{}, usersecurity.RefreshTokens{}, cfg.RefreshTTL)

	s3Client, err := notebooks3.NewClient(ctx)
	if err != nil {
		return nil, startupError("s3_client", err)
	}
	files := notebooks3.NewFileStorage(s3Client, cfg.NotebooksBucket)
	notebookUsecase := notebookusecase.NewNotebookUsecase(
		notebookpostgres.NewNotebookRepository(pool),
		files,
		ipynb.Codec{},
	)
	runtimeModule, err := runtimemodule.Build(ctx, cfg.Runtime, pool)
	if err != nil {
		return nil, startupError("runtime_init", err)
	}
	avatars := usecase.NewAvatarUsecase(userRepo, avatars3.NewStorage(s3Client, cfg.AvatarsBucket), func(ctx context.Context, source io.ReadSeeker, mime string) (usecase.SanitizedAvatar, error) {
		file, codecErr := (avatarcodec.Codec{}).Sanitize(ctx, source, mime)
		switch {
		case errors.Is(codecErr, avatarcodec.ErrInvalidImage):
			return usecase.SanitizedAvatar{}, usecase.ErrInvalidImage
		case errors.Is(codecErr, avatarcodec.ErrUnsupportedMediaType):
			return usecase.SanitizedAvatar{}, usecase.ErrUnsupportedMediaType
		case codecErr != nil:
			return usecase.SanitizedAvatar{}, codecErr
		default:
			return usecase.SanitizedAvatar{Body: file, Size: file.Size, ContentType: file.ContentType}, nil
		}
	})
	runtimeFiles := ipynb.RuntimeFiles{Files: files}
	sessionStore := sessionpostgres.New(pool, runtimeFiles)
	sessionService := sessionusecase.NewService(sessionStore, sessionusecase.Settings{Enabled: cfg.Runtime.Enabled}, nil, runtimeModule.Agent)
	sessionWorker := sessionusecase.NewWorker(sessionStore, sessionenv.New(runtimeModule.Environments), runtimeModule.Agent, runtimeFiles, nil)
	userHandler := userdelivery.NewHandler(userUsecase, userdelivery.WithProfile(avatars, cfg.AvatarsPublicURL))
	srv := httptransport.Server{UserHandler: userHandler, NotebookHandler: notebookdelivery.NewHandler(notebookUsecase), RuntimeHandler: sessiondelivery.NewHandler(sessionService)}
	router, err := httptransport.NewRouter(srv, tokens, sessions, cfg.BFFAPIKey)
	if err != nil {
		return nil, err
	}

	bindAddr := fmt.Sprintf("%s:%s", cfg.Host, cfg.Port)

	httpServer := &http.Server{
		Handler:      router,
		Addr:         bindAddr,
		WriteTimeout: cfg.WriteTimeout,
		ReadTimeout:  cfg.ReadTimeout,
	}

	ready = true
	return &application{
		Server: httpServer, Environments: runtimeModule.Environments, RuntimeWorker: runtimeModule.Worker,
		SessionWorker: sessionWorker, RuntimeMode: cfg.Runtime.Provider, Close: closeResources,
	}, nil
}
