// Command notebook-migrate transfers and verifies notebook documents offline.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/migration"
)

func main() {
	database := flag.String("database-url", os.Getenv("NOTEBOOK_MIGRATION_DATABASE_URL"), "PostgreSQL URL")
	bucket := flag.String("bucket", os.Getenv("MIGRATION_S3_NOTEBOOKS_BUCKET"), "legacy notebook bucket (migration only)")
	verify := flag.Bool("verify", false, "compare all migrated notebooks to S3 without writing")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := migration.RunCommand(ctx, migration.Options{DatabaseURL: *database, Bucket: *bucket, VerifyOnly: *verify}, os.Stdout)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
