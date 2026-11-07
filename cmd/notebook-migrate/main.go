// Command notebook-migrate transfers and verifies notebook documents offline.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/migration"
)

func main() {
	opts, err := migration.ParseOptions(os.Args[1:], os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err = migration.RunCommand(ctx, opts, os.Stdout)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
