package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	notebookcursor "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/cursor"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/ipynb"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
)

func TestResultPreservesRevisionAndAccounting(t *testing.T) {
	repo, _ := testRepository(t)
	ctx := context.Background()
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, notebookcursor.New([]byte("list-test-key")))
	n, err := uc.Create(ctx, 1, "result revision")
	if err != nil {
		t.Fatal(err)
	}
	src, err := repo.ExecutionSource(ctx, n.ID, 1, n.Cells[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	count := int64(12)
	for _, item := range []struct {
		name      string
		outputs   string
		count     *int64
		truncated bool
	}{
		{"first result", `[{"text":"42\n"}]`, nil, false},
		{"outputs only", `[{"text":"longer output\n"}]`, nil, false},
		{"execution count only", `[{"text":"longer output\n"}]`, &count, false},
		{"truncation only", `[{"text":"longer output\n"}]`, &count, true},
		{"clear truncation", `[{"text":"longer output\n"}]`, &count, false},
		{"clear count", `[{"text":"longer output\n"}]`, nil, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			result := usecase.CellResult{CellID: src.CellID, SourceVersion: src.SourceVersion, Outputs: json.RawMessage(item.outputs), ExecutionCount: item.count, OutputTruncated: item.truncated}
			revision, err := repo.SaveResult(ctx, n.ID, 1, result)
			if err != nil {
				t.Fatal(err)
			}
			got, err := repo.Get(ctx, n.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if revision != n.Revision || got.Revision != n.Revision {
				t.Errorf("result changed revision: returned=%q stored=%q want=%q", revision, got.Revision, n.Revision)
			}
			cell := got.Cells[0]
			if string(cell.Outputs) != item.outputs || !reflect.DeepEqual(cell.ExecutionCount, item.count) || cell.OutputTruncated != item.truncated || cell.SourceVersion != src.SourceVersion {
				t.Fatalf("result not saved correctly: %+v", cell)
			}
			raw, err := ipynb.EncodeDocument(got.Document, got.Cells)
			if err != nil || int64(len(raw)) != got.SizeBytes {
				t.Fatalf("result accounting: encoded=%d stored=%d err=%v", len(raw), got.SizeBytes, err)
			}
			if !got.UpdatedAt.After(n.UpdatedAt) {
				t.Fatal("changed result did not update timestamp")
			}
			if _, err = repo.SaveResult(ctx, n.ID, 1, result); err != nil {
				t.Fatal(err)
			}
			noop, err := repo.Get(ctx, n.ID, 1)
			if err != nil || !reflect.DeepEqual(got, noop) {
				t.Fatal("identical result changed notebook", err)
			}
		})
	}
}

func TestEditAndCreateAcceptHashReadBeforeResult(t *testing.T) {
	for _, operation := range []usecase.OperationKind{usecase.Edit, usecase.Insert} {
		t.Run(string(operation), func(t *testing.T) {
			repo, _ := testRepository(t)
			ctx := context.Background()
			uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, notebookcursor.New([]byte("list-test-key")))
			n, err := uc.Create(ctx, 1, "edit after execution")
			if err != nil {
				t.Fatal(err)
			}
			id := n.Cells[0].ID
			edited, err := uc.EditCell(ctx, n.ID, 1, n.Revision, id, "print(42)")
			if err != nil {
				t.Fatal(err)
			}
			src, err := repo.ExecutionSource(ctx, n.ID, 1, id)
			if err != nil {
				t.Fatal(err)
			}
			for _, output := range []string{`[{"text":"42\n"}]`, `[{"text":"43\n"}]`} {
				if _, err = repo.SaveResult(ctx, n.ID, 1, usecase.CellResult{CellID: id, SourceVersion: src.SourceVersion, Outputs: json.RawMessage(output)}); err != nil {
					t.Fatal(err)
				}
			}
			var changeHash string
			if operation == usecase.Edit {
				change, changeErr := uc.EditCell(ctx, n.ID, 1, edited.Hash, id, "print(43)")
				changeHash, err = change.Hash, changeErr
			} else {
				change, changeErr := uc.AddCell(ctx, n.ID, 1, edited.Hash, "code", "print(43)", nil)
				changeHash, err = change.Hash, changeErr
			}
			if err != nil {
				t.Fatalf("%s with hash read before execution: %v", operation, err)
			}
			if changeHash == edited.Hash {
				t.Fatal("source/structure change preserved revision")
			}
			if _, err = uc.EditCell(ctx, n.ID, 1, edited.Hash, id, "stale write"); !errors.Is(err, usecase.ErrNotebookConflict) {
				t.Fatalf("stale editing hash accepted: %v", err)
			}
		})
	}
}

func TestResultMetadataFailureRollsBackData(t *testing.T) {
	repo, pool := testRepository(t)
	ctx := context.Background()
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, notebookcursor.New([]byte("list-test-key")))
	n, err := uc.Create(ctx, 1, "result rollback")
	if err != nil {
		t.Fatal(err)
	}
	src, err := repo.ExecutionSource(ctx, n.ID, 1, n.Cells[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `CREATE FUNCTION reject_result_metadata() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected metadata failure'; END $$; CREATE TRIGGER reject_result_metadata BEFORE UPDATE ON notebooks FOR EACH ROW EXECUTE FUNCTION reject_result_metadata()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.SaveResult(ctx, n.ID, 1, usecase.CellResult{CellID: src.CellID, SourceVersion: src.SourceVersion, Outputs: json.RawMessage(`[{"text":"42"}]`)}); err == nil {
		t.Fatal("metadata failure ignored")
	}
	got, err := repo.Get(ctx, n.ID, 1)
	if err != nil || !reflect.DeepEqual(n, got) {
		t.Fatal("failed result left partial data or accounting", err)
	}
}

func TestExpandedRuntimeOutputRejectedBeforeSQL(t *testing.T) {
	// Около 3 МБ при передаче, но более 147 МиБ в каноническом десятичном виде.
	outputs := json.RawMessage(`[{"output_type":"display_data","data":{"application/json":[` + strings.Repeat(`1e308,`, 499999) + `1e308]}}]`)
	repo := &NotebookRepository{}
	// nil-транзакция намеренна: слишком большой output должен упасть до любого SQL.
	_, err := repo.SaveResultTx(context.Background(), nil, 1, 1, usecase.CellResult{CellID: "a", SourceVersion: "v1", Outputs: outputs})
	if !errors.Is(err, usecase.ErrTooLarge) {
		t.Fatalf("expected size rejection before SQL: %v", err)
	}
}

func TestExpandedRuntimeOutputPreservesNotebook(t *testing.T) {
	repo, _ := testRepository(t)
	ctx := context.Background()
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, notebookcursor.New([]byte("list-test-key")))
	n, err := uc.Create(ctx, 1, "result budget")
	if err != nil {
		t.Fatal(err)
	}
	src, err := repo.ExecutionSource(ctx, n.ID, 1, n.Cells[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	result := usecase.CellResult{CellID: src.CellID, SourceVersion: src.SourceVersion, Outputs: json.RawMessage(`[{"text":"keep"}]`)}
	if _, err = repo.SaveResult(ctx, n.ID, 1, result); err != nil {
		t.Fatal(err)
	}
	before, err := repo.Get(ctx, n.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	result.Outputs = json.RawMessage(`[{"data":{"application/json":[` + strings.Repeat(`1e308,`, 499999) + `1e308]}}]`)
	if _, err = repo.SaveResult(ctx, n.ID, 1, result); !errors.Is(err, usecase.ErrTooLarge) {
		t.Fatalf("size error: %v", err)
	}
	after, err := repo.Get(ctx, n.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected result changed notebook content, revision, timestamps or accounting")
	}
}
