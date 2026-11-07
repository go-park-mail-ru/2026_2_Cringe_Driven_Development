package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/config"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/observability"
)

func startupError(stage string, err error) error {
	switch stage {
	case "database_connect", "database_migrate", "s3_client", "redis_connect", "runtime_init", "http_listen", "notebook_migration", "totp_init":
		return observability.WithCode(err, stage)
	default:
		return observability.WithCode(err, "internal_error")
	}
}

// Run запускает API с настройками окружения и освобождает ресурсы при завершении.
func Run(cfg config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	app, err := newServer(ctx, cfg)
	if err != nil {
		return err
	}
	defer app.Close()
	server := app.Server

	var workers []workerRunner
	if app.RuntimeWorker != nil {
		slog.Info("runtime worker started", slog.String("provider", app.RuntimeMode))
		workers = append(workers, app.RuntimeWorker)
	}
	if app.SessionWorker != nil {
		workers = append(workers, app.SessionWorker)
	}
	stopWorkers := startWorkers(ctx, workers...)
	defer stopWorkers()

	slog.Info("starting server", slog.String("addr", server.Addr), slog.String("write_timeout", server.WriteTimeout.String()), slog.String("read_timeout", server.ReadTimeout.String()))
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return startupError("http_listen", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}

// workerRunner задаёт общий способ запуска и остановки фоновых обработчиков приложения.
type workerRunner interface{ Run(context.Context) error }

// startWorkers отменяет и дожидается всех воркеров до закрытия зависимостей.
func startWorkers(parent context.Context, workers ...workerRunner) func() {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	for _, worker := range workers {
		wg.Go(func() {
			if err := worker.Run(ctx); err != nil && ctx.Err() == nil {
				slog.ErrorContext(ctx, "runtime worker stopped", "error_code", "runtime_worker_failed")
			}
		})
	}
	return func() { cancel(); wg.Wait() }
}
