package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
)

func TestExpandedRuntimeOutputRejectedBeforeSQL(t *testing.T) {
	// About 3 MB on the wire, but over 147 MiB in canonical decimal form.
	outputs := json.RawMessage(`[{"output_type":"display_data","data":{"application/json":[` + strings.Repeat(`1e308,`, 499999) + `1e308]}}]`)
	repo := &NotebookRepository{}
	// A nil transaction is intentional: oversized output must fail before any SQL.
	_, err := repo.SaveResultTx(context.Background(), nil, 1, 1, usecase.CellResult{CellID: "a", SourceVersion: "v1", Outputs: outputs})
	if !errors.Is(err, usecase.ErrTooLarge) {
		t.Fatalf("expected size rejection before SQL: %v", err)
	}
}

func TestExpandedRuntimeOutputPreservesNotebook(t *testing.T) {
	repo, _ := testRepository(t)
	ctx := context.Background()
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{})
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
