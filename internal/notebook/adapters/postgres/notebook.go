// Package postgres транзакционно хранит ноутбуки и отдельные ячейки.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/ipynb"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
}
type NotebookRepository struct{ db DB }

func NewNotebookRepository(db DB) *NotebookRepository { return &NotebookRepository{db: db} }

const selectNotebook = `SELECT id,owner_id,name,revision,cells_count,preview_code,size_bytes,created_at,updated_at FROM notebooks WHERE id=$1 AND owner_id=$2`

func scanNotebook(row pgx.Row) (domain.Notebook, error) {
	var n domain.Notebook
	var revision *string
	err := row.Scan(&n.ID, &n.OwnerID, &n.Name, &revision, &n.CellsCount, &n.PreviewCode, &n.SizeBytes, &n.CreatedAt, &n.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, usecase.ErrNotebookNotFound
	}
	if err != nil {
		return n, err
	}
	// После полного сброса БД у каждого блокнота есть редакция: она создаётся вместе с ним
	if revision == nil {
		return n, errors.New("notebook has no revision")
	}
	n.Revision = *revision
	return n, nil
}
func rawMap(m map[string]json.RawMessage) []byte {
	if m == nil {
		return []byte(`{}`)
	}
	b, _ := json.Marshal(m)
	return b
}
func optionalJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}
func (r *NotebookRepository) Create(ctx context.Context, n domain.Notebook) (domain.Notebook, error) {
	if err := prepare(&n); err != nil {
		return domain.Notebook{}, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Notebook{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = tx.QueryRow(ctx, `INSERT INTO notebooks(owner_id,name,revision,cells_count,preview_code,size_bytes,header_bytes,nbformat,nbformat_minor,metadata,extra) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id,created_at,updated_at`, n.OwnerID, n.Name, n.Revision, n.CellsCount, n.PreviewCode, n.SizeBytes, ipynb.HeaderSize(n.Document), n.Document.NBFormat, n.Document.NBFormatMinor, optionalJSON(n.Document.Metadata), rawMap(n.Document.Extra)).Scan(&n.ID, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		return domain.Notebook{}, err
	}
	for i := range n.Cells {
		if err = insertCell(ctx, tx, n.ID, i, &n.Cells[i]); err != nil {
			return domain.Notebook{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Notebook{}, err
	}
	return n, nil
}
func prepare(n *domain.Notebook) error {
	if len(n.Cells) > usecase.MaxCells {
		return usecase.ErrTooManyCells
	}
	if err := ipynb.NormalizeFields(&n.Document, n.Cells); err != nil {
		return fmt.Errorf("%w: %w", usecase.ErrInvalidDocument, err)
	}
	seen := map[string]bool{}
	sizes := make([]int64, len(n.Cells))
	n.PreviewCode = ""
	for i, c := range n.Cells {
		if c.ID == "" || seen[c.ID] {
			return usecase.ErrInvalidDocument
		}
		seen[c.ID] = true
		if err := usecase.ValidateSource(c.Source); err != nil {
			return err
		}
		if c.Kind != domain.CellKindCode && c.Kind != domain.CellKindMarkdown && c.Kind != "raw" {
			return usecase.ErrInvalidCellKind
		}
		if (c.HasRuntimeMetadata || c.OutputTruncated) && ipynb.RuntimeMetadataOverhead(c) < 0 {
			return usecase.ErrInvalidDocument
		}
		sizes[i] = ipynb.CellSize(n.Cells[i])
		if n.PreviewCode == "" && c.Kind == domain.CellKindCode {
			n.PreviewCode = domain.Preview(c.Source)
		}
	}
	n.SizeBytes = ipynb.DocumentSize(ipynb.HeaderSize(n.Document), sizes)
	if n.SizeBytes > usecase.MaxNotebookBytes {
		return usecase.ErrTooLarge
	}
	n.CellsCount = len(n.Cells)
	n.Revision = uuid.NewString()
	return nil
}
func insertCell(ctx context.Context, tx pgx.Tx, id int64, pos int, c *domain.Cell) error {
	c.SourceVersion = uuid.NewString()
	_, err := tx.Exec(ctx, `INSERT INTO notebook_cells(notebook_id,cell_id,position,kind,source,source_version,metadata,extra,edit_bytes,preview_candidate,runtime_metadata_overhead) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, c.ID, pos, c.Kind, c.Source, c.SourceVersion, optionalJSON(c.Metadata), rawMap(c.Extra), ipynb.LightSize(*c), domain.Preview(c.Source) != "", ipynb.RuntimeMetadataOverhead(*c))
	if err != nil {
		return err
	}
	o, e, a := ipynb.DataSizes(*c)
	_, err = tx.Exec(ctx, `INSERT INTO notebook_cell_data(notebook_id,cell_id,outputs,execution_count,has_execution_count,attachments,outputs_bytes,execution_bytes,attachments_bytes,output_truncated,has_runtime_metadata,runtime_metadata_bytes) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, c.ID, optionalJSON(c.Outputs), c.ExecutionCount, c.HasExecutionCount, optionalJSON(c.Attachments), o, e, a, c.OutputTruncated, c.HasRuntimeMetadata || c.OutputTruncated, ipynb.RuntimeMetadataSize(*c))
	return err
}
func (r *NotebookRepository) Get(ctx context.Context, id, ownerID int64) (domain.Notebook, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Notebook{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY`); err != nil {
		return domain.Notebook{}, err
	}
	n, err := scanNotebook(tx.QueryRow(ctx, selectNotebook, id, ownerID))
	if err != nil {
		return n, err
	}
	n, err = readContent(ctx, tx, n)
	if err != nil {
		return n, err
	}
	if err = tx.Commit(ctx); err != nil {
		return n, err
	}
	return n, nil
}

var _ usecase.Repository = (*NotebookRepository)(nil)
var _ usecase.Results = (*NotebookRepository)(nil)

func readContent(ctx context.Context, tx pgx.Tx, n domain.Notebook) (domain.Notebook, error) {
	var err error

	err = tx.QueryRow(ctx, `SELECT nbformat,nbformat_minor,metadata,extra FROM notebooks WHERE id=$1`, n.ID).Scan(&n.Document.NBFormat, &n.Document.NBFormatMinor, &n.Document.Metadata, &n.Document.Extra)
	if err != nil {
		return n, err
	}
	rows, err := tx.Query(ctx, `SELECT c.cell_id,c.kind,c.source,c.source_version,c.metadata,c.extra,d.outputs,d.execution_count,d.has_execution_count,d.attachments,d.output_truncated,d.has_runtime_metadata FROM notebook_cells c JOIN notebook_cell_data d USING(notebook_id,cell_id) WHERE c.notebook_id=$1 ORDER BY c.position`, n.ID)
	if err != nil {
		return n, err
	}
	n.Cells = []domain.Cell{}
	for rows.Next() {
		var c domain.Cell
		if err = rows.Scan(&c.ID, &c.Kind, &c.Source, &c.SourceVersion, &c.Metadata, &c.Extra, &c.Outputs, &c.ExecutionCount, &c.HasExecutionCount, &c.Attachments, &c.OutputTruncated, &c.HasRuntimeMetadata); err != nil {
			rows.Close()
			return n, err
		}
		n.Cells = append(n.Cells, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return n, err
	}
	if err = ipynb.NormalizeFields(&n.Document, n.Cells); err != nil {
		return n, err
	}
	return n, nil
}
