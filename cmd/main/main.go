package main

import (
	"log/slog"
	"os"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/app"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	if err := app.Run(); err != nil {
		slog.Error("application stopped with error", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
