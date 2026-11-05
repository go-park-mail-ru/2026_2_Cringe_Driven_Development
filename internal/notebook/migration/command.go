package migration

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/postgres"
	notebooks3 "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/s3"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/storage/s3client"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Options struct {
	DatabaseURL, Bucket string
	VerifyOnly          bool
}

func RunCommand(ctx context.Context, opts Options, out io.Writer) error {
	if opts.DatabaseURL == "" || opts.Bucket == "" {
		return errors.New("database-url and bucket are required; stop notebook writes before running")
	}
	pool, err := pgxpool.New(ctx, opts.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if !opts.VerifyOnly {
		if err = migrations.Up(ctx, pool); err != nil {
			return err
		}
	}
	client, err := s3client.NewClient(ctx)
	if err != nil {
		return err
	}
	repo := postgres.NewNotebookRepository(pool)
	stats, err := Run(ctx, repo, notebooks3.NewFileStorage(client, opts.Bucket), opts.VerifyOnly)
	fmt.Fprintf(out, "migrated=%d verified=%d skipped=%d failed=%d\n", stats.Migrated, stats.Verified, stats.Skipped, stats.Failed)
	if err != nil {
		return err
	}
	return repo.Ready(ctx)
}
