package postgres

import (
	"bytes"
	"errors"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/ipynb"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
)

func TestImportNotebookPersistenceAndRollback(t *testing.T) {
	repo, pool := testRepository(t)
	ctx := t.Context()
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, nil, usecase.WithImportDecoder(ipynb.DecodeImport))
	raw := []byte(`{"nbformat":4,"nbformat_minor":5,"metadata":{"a":1},"cells":[{"id":"a","cell_type":"code","source":"print(1)","outputs":[{"text":"1"}],"execution_count":1},{"cell_type":"raw"},{"id":"b","cell_type":"markdown","source":"текст","attachments":{"image":{"image/png":"YWJj"}}}]}`)
	n, skipped, err := uc.Import(ctx, 1, "Imported", raw)
	if err != nil || skipped != 1 || n.CellsCount != 2 || n.PreviewCode != "print(1)" {
		t.Fatalf("notebook=%+v skipped=%d error=%v", n, skipped, err)
	}
	if _, readErr := uc.Get(ctx, n.ID, 2); !errors.Is(readErr, usecase.ErrNotebookNotFound) {
		t.Fatalf("foreign read: %v", readErr)
	}
	page, err := uc.List(ctx, 1, usecase.ListInput{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != n.ID ||
		page.Items[0].CellsCount != 2 || page.Items[0].PreviewCode != "print(1)" {
		t.Fatalf("list=%+v error=%v", page, err)
	}
	content, err := uc.Content(ctx, n.ID, 1)
	if err != nil || content.Hash == "" || len(content.Cells) != 2 || content.Cells[0].SourceVersion == "" {
		t.Fatalf("content=%+v error=%v", content, err)
	}
	stored, err := uc.Get(ctx, n.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ipynb.EncodeDocument(stored.Document, stored.Cells)
	if err != nil {
		t.Fatal(err)
	}
	doc, cells, _, err := ipynb.DecodeImport(raw)
	if err != nil {
		t.Fatal(err)
	}
	want, err := ipynb.EncodeDocument(doc, cells)
	if err != nil || !bytes.Equal(out, want) {
		t.Fatalf("stored document changed: %s; error=%v", out, err)
	}
	// Первая ячейка проходит, вторая нарушает ограничение внутри транзакции.
	if _, err := pool.Exec(ctx, "ALTER TABLE notebook_cells ADD CHECK (position <> 1) NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := uc.Import(ctx, 1, "Rollback", raw); err == nil {
		t.Fatal("expected insert failure")
	}
	var notebooks, cellCount int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM notebooks), (SELECT count(*) FROM notebook_cells)").Scan(&notebooks, &cellCount); err != nil {
		t.Fatal(err)
	}
	if notebooks != 1 || cellCount != 2 {
		t.Fatalf("partial import: notebooks=%d cells=%d", notebooks, cellCount)
	}
}
