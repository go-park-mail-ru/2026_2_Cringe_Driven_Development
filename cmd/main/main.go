package main

import (
	"log/slog"
	"os"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/app"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/config"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/observability"
)

// main запускает приложение и передаёт результат процессу.
func main() {
	var level slog.LevelVar
	level.Set(slog.LevelInfo)
	logger := slog.New(observability.NewHandler(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: &level})))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err == nil {
		level.Set(cfg.LogLevel)
		err = app.Run(cfg)
	} else {
		err = observability.WithCode(err, "startup_config")
	}
	if err != nil {
		slog.Error("application stopped with error", slog.String("error_code", observability.ErrorCode(err)))
		os.Exit(1)
	}
}
