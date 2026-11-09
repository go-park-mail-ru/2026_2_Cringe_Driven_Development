// Команда runtime-smoke — ручная сквозная проверка модуля runtime в облаке (выводит JSON-отчёт).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/config"
	runtimemodule "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/smoke"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	os.Exit(run())
}

func run() int {
	var opts smoke.Options
	flag.StringVar(&opts.Language, "language", "python", "kernel language: python (R is not supported by the runtime agent)")
	flag.IntVar(&opts.Parallel, "parallel", 1, "number of parallel sessions")
	flag.StringVar(&opts.KillWorkerAfter, "kill-worker-after", "", `cancel the worker after "volume" or "server" is saved`)
	flag.DurationVar(&opts.Timeout, "timeout", 30*time.Minute, "overall timeout")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	pool, err := pgxpool.New(ctx, cfg.PostgresURL())
	if err != nil {
		fmt.Fprintln(os.Stderr, "database connect:", err)
		return 1
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "database ping:", err)
		return 1
	}
	deps, cleanup, err := runtimemodule.BuildSmokeDeps(ctx, cfg.Runtime, pool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "runtime:", err)
		return 1
	}
	defer cleanup()
	rep, runErr := smoke.Run(ctx, deps, opts)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err = enc.Encode(rep); err != nil {
		fmt.Fprintln(os.Stderr, "encode report:", err)
		return 1
	}
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "smoke failed:", runErr)
		return 1
	}
	return 0
}
