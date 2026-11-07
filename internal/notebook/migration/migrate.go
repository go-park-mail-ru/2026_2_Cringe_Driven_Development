// Package migration implements the offline, restartable S3-to-cell migration.
package migration

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/ipynb"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/postgres"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

type Repository interface {
	LegacySources(context.Context) ([]postgres.LegacySource, error)
	ImportLegacy(context.Context, postgres.LegacySource, []byte) error
	Get(context.Context, int64, int64) (domain.Notebook, error)
}
type Source interface {
	Get(context.Context, string) ([]byte, error)
}
type Stats struct{ Migrated, Verified, Skipped, Failed int }

// Run never removes source objects and continues after a per-document failure.
func Run(ctx context.Context, repo Repository, source Source, verifyOnly bool) (Stats, error) {
	var stats Stats
	sources, err := repo.LegacySources(ctx)
	if err != nil {
		return stats, err
	}
	var failures []error
	for _, s := range sources {
		if err = ctx.Err(); err != nil {
			return stats, errors.Join(append(failures, err)...)
		}
		if s.Verified && !verifyOnly {
			stats.Skipped++
			continue
		}
		err = process(ctx, repo, source, s, verifyOnly)
		if err != nil {
			stats.Failed++
			failures = append(failures, fmt.Errorf("notebook %d: %w", s.NotebookID, err))
			continue
		}
		if verifyOnly {
			stats.Verified++
		} else {
			stats.Migrated++
		}
	}
	return stats, errors.Join(failures...)
}
func process(ctx context.Context, repo Repository, source Source, s postgres.LegacySource, verifyOnly bool) error {
	data, err := source.Get(ctx, s.FileKey)
	if err != nil {
		return err
	}
	if !verifyOnly {
		return repo.ImportLegacy(ctx, s, data)
	}
	if !s.Verified {
		return errors.New("notebook has not been migrated")
	}
	doc, cells, err := ipynb.DecodeDocument(data)
	if err != nil {
		return err
	}
	expected, err := ipynb.EncodeDocument(doc, cells)
	if err != nil {
		return err
	}
	n, err := repo.Get(ctx, s.NotebookID, s.OwnerID)
	if err != nil {
		return err
	}
	actual, err := ipynb.EncodeDocument(n.Document, n.Cells)
	if err != nil {
		return err
	}
	if !bytes.Equal(expected, actual) || int64(len(actual)) != n.SizeBytes {
		return errors.New("semantic export/accounting mismatch")
	}
	return nil
}
