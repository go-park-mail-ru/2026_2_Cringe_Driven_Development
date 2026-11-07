package migration

import (
	"context"
	"errors"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/postgres"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

type fakeRepository struct {
	sources  []postgres.LegacySource
	imported []int64
	fail     bool
}

func (f *fakeRepository) LegacySources(context.Context) ([]postgres.LegacySource, error) {
	return f.sources, nil
}
func (f *fakeRepository) ImportLegacy(_ context.Context, s postgres.LegacySource, _ []byte) error {
	if f.fail && s.NotebookID == 2 {
		return errors.New("bad document")
	}
	f.imported = append(f.imported, s.NotebookID)
	return nil
}
func (f *fakeRepository) Get(context.Context, int64, int64) (domain.Notebook, error) {
	return domain.Notebook{}, nil
}

type files struct{}

func (files) Get(context.Context, string) ([]byte, error) { return []byte(`{}`), nil }
func TestMigrationContinuesAndSkipsVerified(t *testing.T) {
	repo := &fakeRepository{fail: true, sources: []postgres.LegacySource{{NotebookID: 1, Verified: true}, {NotebookID: 2}, {NotebookID: 3}}}
	stats, err := Run(context.Background(), repo, files{}, false)
	if err == nil || stats.Skipped != 1 || stats.Failed != 1 || stats.Migrated != 1 || len(repo.imported) != 1 || repo.imported[0] != 3 {
		t.Fatalf("%+v %v imported=%v", stats, err, repo.imported)
	}
}
