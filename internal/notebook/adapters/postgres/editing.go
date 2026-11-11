package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/ipynb"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// target не содержит JSONB-значений; даже очень большие вложения остаются в PostgreSQL.
type target struct {
	runtimeBytes, runtimeOverhead         int64
	cell                                  domain.Cell
	position                              int
	edit, outputs, execution, attachments int64
}

func findTarget(ctx context.Context, tx pgx.Tx, id int64, op usecase.Operation) (target, error) {
	var t target
	predicate := "c.cell_id=$2"
	var value any = op.CellID
	if op.CellID == "" && op.Index != nil {
		predicate = "c.position=$2"
		value = *op.Index
	}
	err := tx.QueryRow(ctx, `SELECT c.cell_id,c.kind,c.source,c.source_version,c.position,c.edit_bytes,d.outputs_bytes,d.execution_bytes,d.attachments_bytes,d.runtime_metadata_bytes,c.runtime_metadata_overhead,d.has_runtime_metadata FROM notebook_cells c JOIN notebook_cell_data d USING(notebook_id,cell_id) WHERE c.notebook_id=$1 AND `+predicate, id, value).Scan(&t.cell.ID, &t.cell.Kind, &t.cell.Source, &t.cell.SourceVersion, &t.position, &t.edit, &t.outputs, &t.execution, &t.attachments, &t.runtimeBytes, &t.runtimeOverhead, &t.cell.HasRuntimeMetadata)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, usecase.ErrCellNotFound
	}
	return t, err
}
func (r *NotebookRepository) Apply(ctx context.Context, id, ownerID int64, expected string, op usecase.Operation) (usecase.Change, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return usecase.Change{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	n, err := scanNotebook(tx.QueryRow(ctx, selectNotebook+` FOR UPDATE`, id, ownerID))
	if err != nil {
		return usecase.Change{}, err
	}
	if expected == "" {
		return usecase.Change{}, usecase.ErrInvalidHash
	}
	if expected != n.Revision {
		return usecase.Change{}, usecase.ErrNotebookConflict
	}
	result := usecase.Change{Hash: n.Revision}
	if op.Kind == usecase.Insert {
		if n.CellsCount >= usecase.MaxCells {
			return result, usecase.ErrTooManyCells
		}
		if op.CellKind != domain.CellKindCode && op.CellKind != domain.CellKindMarkdown {
			return result, usecase.ErrInvalidCellKind
		}
		if err = usecase.ValidateSource(op.Source); err != nil {
			return result, err
		}
		if op.CellID == "" || strings.ContainsRune(op.CellID, 0) {
			return result, usecase.ErrInvalidDocument
		}
		pos := n.CellsCount
		if op.Index != nil {
			pos = *op.Index
		}
		if pos < 0 || pos > n.CellsCount {
			return result, usecase.ErrIndexOutOfRange
		}
		c := domain.Cell{ID: op.CellID, Kind: op.CellKind, Source: op.Source, Metadata: json.RawMessage(`{}`)}
		if c.Kind == domain.CellKindCode {
			c.Outputs = json.RawMessage(`[]`)
			c.HasExecutionCount = true
		}
		delta := ipynb.CellSize(c)
		if n.CellsCount > 0 {
			delta++
		}
		n.SizeBytes += delta
		n.CellsCount++
		if n.SizeBytes > usecase.MaxNotebookBytes {
			return result, usecase.ErrTooLarge
		}
		if _, err = tx.Exec(ctx, `UPDATE notebook_cells SET position=position+1 WHERE notebook_id=$1 AND position >= $2`, id, pos); err != nil {
			return result, err
		}
		if err = insertCell(ctx, tx, id, pos, &c); err != nil {
			return result, err
		}
		result.Cell = c
	} else {
		t, e := findTarget(ctx, tx, id, op)
		if e != nil {
			return result, e
		}
		result.Cell = t.cell
		switch op.Kind {
		case usecase.Edit:
			if err = usecase.ValidateSource(op.Source); err != nil {
				return result, err
			}
			if op.Source == t.cell.Source {
				return result, nil
			}
			old, _ := json.Marshal(t.cell.Source)
			updated, _ := json.Marshal(op.Source)
			delta := int64(len(updated) - len(old))
			t.cell.Source = op.Source
			t.cell.SourceVersion = uuid.NewString()
			if _, err = tx.Exec(ctx, `UPDATE notebook_cells SET source=$3,source_version=$4,edit_bytes=edit_bytes+$5,preview_candidate=$6 WHERE notebook_id=$1 AND cell_id=$2`, id, t.cell.ID, op.Source, t.cell.SourceVersion, delta, domain.Preview(op.Source) != ""); err != nil {
				return result, err
			}
			if t.cell.Kind == domain.CellKindCode {
				t.cell.Outputs = json.RawMessage(`[]`)
				t.cell.HasExecutionCount = true
				o, e, _ := ipynb.DataSizes(t.cell)
				runtimeBytes := int64(0)
				if t.cell.HasRuntimeMetadata {
					runtimeBytes = t.runtimeOverhead + 5
				}
				delta += o + e - t.outputs - t.execution + runtimeBytes - t.runtimeBytes
				_, err = tx.Exec(ctx, `UPDATE notebook_cell_data SET outputs='[]',output_truncated=false,execution_count=NULL,has_execution_count=true,outputs_bytes=$3,execution_bytes=$4,runtime_metadata_bytes=$5 WHERE notebook_id=$1 AND cell_id=$2`, id, t.cell.ID, o, e, runtimeBytes)
				if err != nil {
					return result, err
				}
			}
			n.SizeBytes += delta
			result.Cell = t.cell
		case usecase.Move:
			if op.Index == nil || *op.Index < 0 || *op.Index >= n.CellsCount {
				return result, usecase.ErrIndexOutOfRange
			}
			to := *op.Index
			if to == t.position {
				return result, nil
			}
			if to < t.position {
				_, err = tx.Exec(ctx, `UPDATE notebook_cells SET position=CASE WHEN cell_id=$2 THEN $3 ELSE position+1 END WHERE notebook_id=$1 AND position BETWEEN $3 AND $4`, id, t.cell.ID, to, t.position)
			} else {
				_, err = tx.Exec(ctx, `UPDATE notebook_cells SET position=CASE WHEN cell_id=$2 THEN $3 ELSE position-1 END WHERE notebook_id=$1 AND position BETWEEN $4 AND $3`, id, t.cell.ID, to, t.position)
			}
			if err != nil {
				return result, err
			}
		case usecase.Delete:
			if _, err = tx.Exec(ctx, `DELETE FROM notebook_cells WHERE notebook_id=$1 AND cell_id=$2`, id, t.cell.ID); err != nil {
				return result, err
			}
			if _, err = tx.Exec(ctx, `UPDATE notebook_cells SET position=position-1 WHERE notebook_id=$1 AND position>$2`, id, t.position); err != nil {
				return result, err
			}
			n.SizeBytes -= t.edit + t.outputs + t.execution + t.attachments + t.runtimeBytes
			if n.CellsCount > 1 {
				n.SizeBytes--
			}
			n.CellsCount--
		default:
			return result, usecase.ErrInvalidDocument
		}
	}
	if n.SizeBytes > usecase.MaxNotebookBytes {
		return result, usecase.ErrTooLarge
	}
	n.PreviewCode, err = preview(ctx, tx, id)
	if err != nil {
		return result, err
	}
	result.Hash, err = publish(ctx, tx, n)
	if err != nil {
		return result, err
	}
	if err = tx.Commit(ctx); err != nil {
		return usecase.Change{}, err
	}
	return result, nil
}
func preview(ctx context.Context, tx pgx.Tx, id int64) (string, error) {
	var source string
	err := tx.QueryRow(ctx, `SELECT source FROM notebook_cells WHERE notebook_id=$1 AND kind='code' AND preview_candidate ORDER BY position LIMIT 1`, id).Scan(&source)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return domain.Preview(source), nil
}

func publish(ctx context.Context, tx pgx.Tx, n domain.Notebook) (string, error) {
	if n.SizeBytes > usecase.MaxNotebookBytes {
		return "", usecase.ErrTooLarge
	}
	revision := uuid.NewString()
	_, err := tx.Exec(ctx, `UPDATE notebooks SET revision=$2,cells_count=$3,preview_code=$4,size_bytes=$5,updated_at=clock_timestamp() WHERE id=$1`, n.ID, revision, n.CellsCount, n.PreviewCode, n.SizeBytes)
	return revision, err
}
