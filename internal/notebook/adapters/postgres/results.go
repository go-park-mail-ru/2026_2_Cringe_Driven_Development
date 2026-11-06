package postgres

import (
	"context"
	"errors"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/ipynb"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/jackc/pgx/v5"
)

func (r *NotebookRepository) ExecutionSource(ctx context.Context, id, ownerID int64, cellID string) (usecase.ExecutionSource, error) {
	var out usecase.ExecutionSource
	err := r.db.QueryRow(ctx, `SELECT c.cell_id,c.source,c.source_version FROM notebook_cells c JOIN notebooks n ON n.id=c.notebook_id WHERE n.id=$1 AND n.owner_id=$2 AND c.cell_id=$3 AND c.kind='code'`, id, ownerID, cellID).Scan(&out.CellID, &out.Source, &out.SourceVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, usecase.ErrCellNotFound
	}
	return out, err
}
func (r *NotebookRepository) SaveResult(ctx context.Context, id, ownerID int64, result usecase.CellResult) (string, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	revision, err := r.SaveResultTx(ctx, tx, id, ownerID, result)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return revision, nil
}

// SaveResultTx applies a result within the caller's PostgreSQL transaction.
// It uses the same notebook lock, source-version check and size accounting as SaveResult.
func (r *NotebookRepository) SaveResultTx(ctx context.Context, tx pgx.Tx, id, ownerID int64, result usecase.CellResult) (string, error) {

	if result.SourceVersion == "" {
		return "", usecase.ErrStaleSource
	}
	if len(result.Outputs) > usecase.MaxNotebookBytes {
		return "", usecase.ErrTooLarge
	}
	outputs, err := ipynb.CanonicalJSONLimit(result.Outputs, usecase.MaxNotebookBytes)
	if errors.Is(err, domain.ErrTooLarge) {
		return "", usecase.ErrTooLarge
	}
	if err != nil {
		return "", usecase.ErrInvalidDocument
	}
	if len(outputs) > usecase.MaxNotebookBytes {
		return "", usecase.ErrTooLarge
	}
	if err = ipynb.ValidateOutputs(outputs); err != nil {
		return "", usecase.ErrInvalidDocument
	}

	n, err := scanNotebook(tx.QueryRow(ctx, selectNotebook+` FOR UPDATE`, id, ownerID))
	if err != nil {
		return "", err
	}
	t, err := findTarget(ctx, tx, id, usecase.Operation{CellID: result.CellID})
	if errors.Is(err, usecase.ErrCellNotFound) {
		return "", usecase.ErrStaleSource
	}
	if err != nil {
		return "", err
	}
	if t.cell.Kind != domain.CellKindCode || t.cell.SourceVersion != result.SourceVersion {
		return "", usecase.ErrStaleSource
	}
	if t.runtimeOverhead < 0 {
		return "", usecase.ErrInvalidDocument
	}
	var same bool
	err = tx.QueryRow(ctx, `SELECT COALESCE(outputs=$3::jsonb AND output_truncated=$5 AND has_runtime_metadata AND has_execution_count AND execution_count IS NOT DISTINCT FROM $4::bigint,false) FROM notebook_cell_data WHERE notebook_id=$1 AND cell_id=$2`, id, result.CellID, []byte(outputs), result.ExecutionCount, result.OutputTruncated).Scan(&same)
	// SQL NULL outputs are distinct from an explicit empty result.
	if err != nil {
		return "", err
	}
	if same {
		return n.Revision, nil
	}
	c := domain.Cell{Outputs: outputs, ExecutionCount: result.ExecutionCount, HasExecutionCount: true}
	o, e, _ := ipynb.DataSizes(c)
	runtimeBytes := t.runtimeOverhead + 5
	if result.OutputTruncated {
		runtimeBytes--
	}
	n.SizeBytes += o + e - t.outputs - t.execution + runtimeBytes - t.runtimeBytes
	if n.SizeBytes > usecase.MaxNotebookBytes {
		return "", usecase.ErrTooLarge
	}
	_, err = tx.Exec(ctx, `UPDATE notebook_cell_data SET outputs=$3,execution_count=$4,output_truncated=$7,has_runtime_metadata=true,runtime_metadata_bytes=$8,has_execution_count=true,outputs_bytes=$5,execution_bytes=$6 WHERE notebook_id=$1 AND cell_id=$2`, id, result.CellID, []byte(outputs), result.ExecutionCount, o, e, result.OutputTruncated, runtimeBytes)
	if err != nil {
		return "", err
	}
	revision, err := publish(ctx, tx, n)
	if err != nil {
		return "", err
	}
	return revision, nil
}
