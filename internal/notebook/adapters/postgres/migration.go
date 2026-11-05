package postgres

import (
	"bytes"
	"context"
	"errors"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/ipynb"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/jackc/pgx/v5"
)

// LegacySource is used exclusively by the offline migration command.
type LegacySource struct {
	NotebookID, OwnerID int64
	FileKey             string
	Verified            bool
}

func (r *NotebookRepository) LegacySources(ctx context.Context) ([]LegacySource, error) {
	rows, err := r.db.Query(ctx, `SELECT l.notebook_id,n.owner_id,l.file_key,l.verified_at IS NOT NULL FROM notebook_legacy_sources l JOIN notebooks n ON n.id=l.notebook_id ORDER BY l.notebook_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LegacySource{}
	for rows.Next() {
		var s LegacySource
		if err = rows.Scan(&s.NotebookID, &s.OwnerID, &s.FileKey, &s.Verified); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ImportLegacy atomically installs and verifies one document, preserving identity
// and dates. An already verified notebook is never overwritten on a retry.
func (r *NotebookRepository) ImportLegacy(ctx context.Context, src LegacySource, data []byte) error {
	doc, cells, err := ipynb.DecodeDocument(data)
	if err != nil {
		return err
	}
	n := domain.Notebook{ID: src.NotebookID, OwnerID: src.OwnerID, Document: doc, Cells: cells}
	if err = prepare(&n); err != nil {
		return err
	}
	expected, err := ipynb.EncodeDocument(n.Document, n.Cells)
	if err != nil {
		return err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var revision *string
	var ownerID int64
	err = tx.QueryRow(ctx, `SELECT owner_id,revision FROM notebooks WHERE id=$1 FOR UPDATE`, n.ID).Scan(&ownerID, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return usecase.ErrNotebookNotFound
	}
	if err != nil {
		return err
	}
	if ownerID != src.OwnerID {
		return usecase.ErrNotebookNotFound
	}
	var verified bool
	if err = tx.QueryRow(ctx, `SELECT verified_at IS NOT NULL FROM notebook_legacy_sources WHERE notebook_id=$1 AND file_key=$2`, n.ID, src.FileKey).Scan(&verified); err != nil {
		return err
	}
	if verified {
		return nil
	}
	if revision != nil {
		return errors.New("unverified notebook already has cell storage")
	}
	for i := range n.Cells {
		if err = insertCell(ctx, tx, n.ID, i, &n.Cells[i]); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE notebooks SET revision=$2,cells_count=$3,preview_code=$4,size_bytes=$5,header_bytes=$6,nbformat=$7,nbformat_minor=$8,metadata=$9,extra=$10 WHERE id=$1`, n.ID, n.Revision, n.CellsCount, n.PreviewCode, n.SizeBytes, ipynb.HeaderSize(n.Document), n.Document.NBFormat, n.Document.NBFormatMinor, optionalJSON(n.Document.Metadata), rawMap(n.Document.Extra))
	if err != nil {
		return err
	}
	// Read back all persisted fields inside the same transaction before marking it verified.
	actual, err := readContent(ctx, tx, n)
	if err != nil {
		return err
	}
	export, err := ipynb.EncodeDocument(actual.Document, actual.Cells)
	if err != nil {
		return err
	}
	if !bytes.Equal(expected, export) || int64(len(export)) != n.SizeBytes {
		return errors.New("semantic export/accounting verification failed")
	}
	if _, err = tx.Exec(ctx, `UPDATE notebook_legacy_sources SET verified_at=clock_timestamp() WHERE notebook_id=$1`, n.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Ready prevents the ordinary API from starting before offline migration finishes.
func (r *NotebookRepository) Ready(ctx context.Context) error {
	var pending bool
	if err := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notebooks WHERE revision IS NULL)`).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return usecase.ErrMigrationRequired
	}
	return nil
}
