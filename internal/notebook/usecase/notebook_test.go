package usecase

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/ipynb"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/models"
	"github.com/google/go-cmp/cmp"
)

const owner = int64(1)

// kinds сокращает блоки до их видов.
func kinds(cells []models.Cell) []models.CellKind {
	out := make([]models.CellKind, 0, len(cells))
	for _, c := range cells {
		out = append(out, c.Kind)
	}
	return out
}

func ids(cells []models.Cell) []string {
	out := make([]string, 0, len(cells))
	for _, c := range cells {
		out = append(out, c.ID)
	}
	return out
}

func TestCreate(t *testing.T) {
	uc, _, files := newTestUsecase()
	ctx := context.Background()

	n, err := uc.Create(ctx, owner, "  Анализ данных  ")
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "Анализ данных" {
		t.Errorf("name = %q, пробелы по краям не обрезаны", n.Name)
	}
	if diff := cmp.Diff([]models.CellKind{models.CellKindCode}, kinds(n.Cells)); diff != "" {
		t.Errorf("новый блокнот (-want +got):\n%s", diff)
	}
	if n.CellsCount != 1 {
		t.Errorf("cells_count = %d, want 1", n.CellsCount)
	}
	if _, ok := files.data[n.FileKey]; !ok {
		t.Errorf("файл %s не записан", n.FileKey)
	}

	got, err := uc.Get(ctx, n.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(n.Cells, got.Cells); diff != "" {
		t.Errorf("Get() после Create (-want +got):\n%s", diff)
	}
}

func TestCreateErrors(t *testing.T) {
	t.Run("имя из одних пробелов", func(t *testing.T) {
		uc, _, _ := newTestUsecase()
		if _, err := uc.Create(context.Background(), owner, "   "); !errors.Is(err, ErrEmptyName) {
			t.Errorf("Create() error = %v, want ErrEmptyName", err)
		}
	})
	t.Run("S3 недоступен: строки в базе нет", func(t *testing.T) {
		uc, meta, files := newTestUsecase()
		files.down = true
		if _, err := uc.Create(context.Background(), owner, "a"); !errors.Is(err, errS3Down) {
			t.Fatalf("Create() error = %v, want errS3Down", err)
		}
		if len(meta.notebooks) != 0 {
			t.Error("при ошибке S3 в базе осталась строка блокнота")
		}
	})
}

func TestCreateCell(t *testing.T) {
	idx := func(i int) *int { return &i }
	tests := []struct {
		name    string
		index   *int
		want    []string
		wantErr error
	}{
		{name: "без index — в конец", want: []string{"c1", "c3"}},
		{name: "index 0 — в начало", index: idx(0), want: []string{"c3", "c1"}},
		{name: "index равен длине — в конец", index: idx(1), want: []string{"c1", "c3"}},
		{name: "index за концом", index: idx(2), want: []string{"c1"}, wantErr: ErrIndexOutOfRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uc, meta, _ := newTestUsecase()
			ctx := context.Background()
			n, err := uc.Create(ctx, owner, "a")
			if err != nil {
				t.Fatal(err)
			}

			cell, err := uc.CreateCell(ctx, n.ID, owner, models.CellKindMarkdown, tt.index)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("CreateCell() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && (cell.Kind != models.CellKindMarkdown || cell.Source != "") {
				t.Errorf("CreateCell() = %+v, want пустой markdown", cell)
			}
			got, err := uc.Get(ctx, n.ID, owner)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, ids(got.Cells)); diff != "" {
				t.Errorf("блоки после CreateCell (-want +got):\n%s", diff)
			}
			if meta.notebooks[n.ID].CellsCount != len(tt.want) {
				t.Errorf("cells_count = %d, want %d", meta.notebooks[n.ID].CellsCount, len(tt.want))
			}
		})
	}
}

func TestDeleteCell(t *testing.T) {
	uc, meta, _ := newTestUsecase()
	ctx := context.Background()
	n, err := uc.Create(ctx, owner, "a")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []models.CellKind{models.CellKindMarkdown, models.CellKindCode} {
		if _, err = uc.CreateCell(ctx, n.ID, owner, kind, nil); err != nil {
			t.Fatal(err)
		}
	}

	if err = uc.DeleteCell(ctx, n.ID, owner, 1); err != nil {
		t.Fatal(err)
	}
	got, err := uc.Get(ctx, n.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"c1", "c4"}, ids(got.Cells)); diff != "" {
		t.Errorf("блоки после удаления (-want +got):\n%s", diff)
	}
	if meta.notebooks[n.ID].CellsCount != 2 {
		t.Errorf("cells_count = %d, want 2", meta.notebooks[n.ID].CellsCount)
	}

	if err = uc.DeleteCell(ctx, n.ID, owner, 2); !errors.Is(err, ErrCellNotFound) {
		t.Errorf("DeleteCell() за концом: error = %v, want ErrCellNotFound", err)
	}

	for range 2 {
		if err = uc.DeleteCell(ctx, n.ID, owner, 0); err != nil {
			t.Fatal(err)
		}
	}
	got, err = uc.Get(ctx, n.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cells == nil || len(got.Cells) != 0 {
		t.Errorf("cells = %#v, want пустой срез", got.Cells)
	}
}

func TestNotFound(t *testing.T) {
	uc, _, _ := newTestUsecase()
	ctx := context.Background()
	n, createErr := uc.Create(ctx, owner, "a")
	if createErr != nil {
		t.Fatal(createErr)
	}
	const stranger = owner + 1

	tests := []struct {
		name string
		call func() error
	}{
		{name: "Get чужого", call: func() error { _, err := uc.Get(ctx, n.ID, stranger); return err }},
		{name: "Get несуществующего", call: func() error { _, err := uc.Get(ctx, 999, owner); return err }},
		{name: "CreateCell чужого", call: func() error {
			_, err := uc.CreateCell(ctx, n.ID, stranger, models.CellKindCode, nil)
			return err
		}},
		{name: "DeleteCell чужого", call: func() error { return uc.DeleteCell(ctx, n.ID, stranger, 0) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !errors.Is(err, ErrNotebookNotFound) {
				t.Errorf("error = %v, want ErrNotebookNotFound", err)
			}
		})
	}

	list, err := uc.List(ctx, stranger)
	if err != nil || list == nil || len(list) != 0 {
		t.Errorf("List() чужого = %#v, %v; want [], nil", list, err)
	}
}

func TestTooManyCells(t *testing.T) {
	uc, _, files := newTestUsecase()
	ctx := context.Background()
	n, err := uc.Create(ctx, owner, "a")
	if err != nil {
		t.Fatal(err)
	}
	full := make([]models.Cell, MaxCells)
	for i := range full {
		full[i] = models.Cell{ID: "x" + strconv.Itoa(i), Kind: models.CellKindCode}
	}
	if files.data[n.FileKey], err = ipynb.Build(full); err != nil {
		t.Fatal(err)
	}
	if _, err = uc.CreateCell(ctx, n.ID, owner, models.CellKindCode, nil); !errors.Is(err, ErrTooManyCells) {
		t.Errorf("CreateCell() сверх предела: error = %v, want ErrTooManyCells", err)
	}
}

func TestS3ErrorKeepsCellsCount(t *testing.T) {
	uc, meta, files := newTestUsecase()
	ctx := context.Background()
	n, err := uc.Create(ctx, owner, "a")
	if err != nil {
		t.Fatal(err)
	}
	files.down = true

	if _, err = uc.CreateCell(ctx, n.ID, owner, models.CellKindCode, nil); !errors.Is(err, errS3Down) {
		t.Fatalf("CreateCell() error = %v, want errS3Down", err)
	}
	if meta.notebooks[n.ID].CellsCount != 1 {
		t.Errorf("cells_count = %d после ошибки S3, want 1", meta.notebooks[n.ID].CellsCount)
	}
}
