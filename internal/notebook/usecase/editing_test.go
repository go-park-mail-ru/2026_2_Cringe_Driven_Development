package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

func TestContentEditingLifecycleAndConflicts(t *testing.T) {
	uc, _, _ := newTestUsecase()
	ctx := context.Background()
	n, err := uc.Create(ctx, owner, "editing")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := uc.Content(ctx, n.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Hash == "" || len(snapshot.Cells) != 1 {
		t.Fatalf("invalid snapshot: %+v", snapshot)
	}
	added, err := uc.AddCell(ctx, n.ID, owner, snapshot.Hash, domain.CellKindMarkdown, "# title", nil)
	if err != nil {
		t.Fatal(err)
	}
	if added.Hash == snapshot.Hash || added.Cell.Source != "# title" {
		t.Fatalf("invalid add: %+v", added)
	}
	if _, err = uc.EditCell(ctx, n.ID, owner, snapshot.Hash, "missing", "wrong"); !errors.Is(err, ErrNotebookConflict) {
		t.Fatalf("stale edit error: %v", err)
	}
	edited, err := uc.EditCell(ctx, n.ID, owner, added.Hash, added.Cell.ID, "# edited")
	if err != nil {
		t.Fatal(err)
	}
	moved, err := uc.MoveCell(ctx, n.ID, owner, edited.Hash, added.Cell.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = uc.Content(ctx, n.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Cells[0].ID != added.Cell.ID || snapshot.Cells[0].Kind != domain.CellKindMarkdown || snapshot.Hash != moved {
		t.Fatalf("bad move: %+v", snapshot)
	}
	hash, err := uc.RemoveCell(ctx, n.ID, owner, moved, added.Cell.ID)
	if err != nil {
		t.Fatal(err)
	}
	hash, err = uc.RemoveCell(ctx, n.ID, owner, hash, n.Cells[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = uc.Content(ctx, n.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Hash != hash || snapshot.Cells == nil || len(snapshot.Cells) != 0 {
		t.Fatalf("invalid empty notebook: %+v", snapshot)
	}
}

func TestConcurrentEditingRejectsSecondWriter(t *testing.T) {
	uc, _, _ := newTestUsecase()
	ctx := context.Background()
	n, err := uc.Create(ctx, owner, "concurrent")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := uc.Content(ctx, n.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, source := range []string{"first", "second"} {
		wg.Go(func() {
			<-start
			_, err := uc.EditCell(ctx, n.ID, owner, snapshot.Hash, n.Cells[0].ID, source)
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrNotebookConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestNoOpEditingKeepsRevision(t *testing.T) {
	uc, meta, files := newTestUsecase()
	ctx := context.Background()
	n, err := uc.Create(ctx, owner, "noop")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := uc.Content(ctx, n.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	before := meta.notebooks[n.ID]
	writes := len(files.data)
	result, err := uc.EditCell(ctx, n.ID, owner, snapshot.Hash, n.Cells[0].ID, "")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := uc.MoveCell(ctx, n.ID, owner, result.Hash, n.Cells[0].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	after := meta.notebooks[n.ID]
	if hash != snapshot.Hash || len(files.data) != writes || after.FileKey != before.FileKey || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("no-op created a new revision")
	}
}

func TestOldSnapshotAndLegacyMutations(t *testing.T) {
	uc, meta, files := newTestUsecase()
	ctx := context.Background()
	n, err := uc.Create(ctx, owner, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	old := string(files.data[n.FileKey])
	snapshot, err := uc.Content(ctx, n.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = uc.CreateCell(ctx, n.ID, owner, testRevision(t, uc, n.ID, owner), domain.CellKindCode, nil); err != nil {
		t.Fatal(err)
	}
	if meta.notebooks[n.ID].FileKey == n.FileKey || string(files.data[n.FileKey]) != old {
		t.Fatal("legacy add overwrote old snapshot")
	}
	if _, err = uc.EditCell(ctx, n.ID, owner, snapshot.Hash, n.Cells[0].ID, "stale"); !errors.Is(err, ErrNotebookConflict) {
		t.Fatalf("legacy mutation did not invalidate hash: %v", err)
	}
	if err = uc.DeleteCell(ctx, n.ID, owner, testRevision(t, uc, n.ID, owner), 0); err != nil {
		t.Fatal(err)
	}
}

func TestFailedPublicationKeepsCurrentRevision(t *testing.T) {
	for _, stage := range []string{"s3", "commit"} {
		t.Run(stage, func(t *testing.T) {
			uc, meta, files := newTestUsecase()
			ctx := context.Background()
			n, err := uc.Create(ctx, owner, "failure")
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := uc.Content(ctx, n.ID, owner)
			if err != nil {
				t.Fatal(err)
			}
			before := meta.notebooks[n.ID]
			old := string(files.data[before.FileKey])
			failure := errors.New("commit failed")
			if stage == "s3" {
				files.down = true
				failure = errS3Down
			} else {
				meta.modifyErr = failure
			}
			result, err := uc.EditCell(ctx, n.ID, owner, snapshot.Hash, n.Cells[0].ID, "changed")
			if !errors.Is(err, failure) || result.Hash != "" {
				t.Fatalf("failure reported as success: %+v %v", result, err)
			}
			after := meta.notebooks[n.ID]
			if after.FileKey != before.FileKey || after.CellsCount != before.CellsCount || !after.UpdatedAt.Equal(before.UpdatedAt) || string(files.data[before.FileKey]) != old {
				t.Fatal("failed publication changed current revision")
			}
			current, err := uc.Content(ctx, n.ID, owner)
			if err != nil {
				t.Fatal(err)
			}
			if current.Hash != snapshot.Hash || current.Cells[0].Source != "" {
				t.Fatal("failed publication leaked new data")
			}
		})
	}
}

func TestEditingRejectsInvalidOperationsWithoutWriting(t *testing.T) {
	uc, _, files := newTestUsecase()
	ctx := context.Background()
	n, err := uc.Create(ctx, owner, "bounds")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := uc.Content(ctx, n.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	index := func(i int) *int { return &i }
	tests := []struct {
		name string
		call func() error
		want error
	}{
		{"missing hash", func() error { _, err := uc.EditCell(ctx, n.ID, owner, "", n.Cells[0].ID, "x"); return err }, ErrInvalidHash},
		{"missing cell", func() error { _, err := uc.EditCell(ctx, n.ID, owner, snapshot.Hash, "missing", "x"); return err }, ErrCellNotFound},
		{"negative move", func() error { _, err := uc.MoveCell(ctx, n.ID, owner, snapshot.Hash, n.Cells[0].ID, -1); return err }, ErrIndexOutOfRange},
		{"move past end", func() error { _, err := uc.MoveCell(ctx, n.ID, owner, snapshot.Hash, n.Cells[0].ID, 1); return err }, ErrIndexOutOfRange},
		{"missing move", func() error { _, err := uc.MoveCell(ctx, n.ID, owner, snapshot.Hash, "missing", 0); return err }, ErrCellNotFound},
		{"missing removal", func() error { _, err := uc.RemoveCell(ctx, n.ID, owner, snapshot.Hash, "missing"); return err }, ErrCellNotFound},
		{"negative insert", func() error {
			_, err := uc.AddCell(ctx, n.ID, owner, snapshot.Hash, domain.CellKindCode, "x", index(-1))
			return err
		}, ErrIndexOutOfRange},
		{"insert past end", func() error {
			_, err := uc.AddCell(ctx, n.ID, owner, snapshot.Hash, domain.CellKindCode, "x", index(2))
			return err
		}, ErrIndexOutOfRange},
		{"invalid kind", func() error { _, err := uc.AddCell(ctx, n.ID, owner, snapshot.Hash, "raw", "x", nil); return err }, ErrInvalidCellKind},
		{"foreign read", func() error { _, err := uc.Content(ctx, n.ID, owner+1); return err }, ErrNotebookNotFound},
		{"foreign edit", func() error { _, err := uc.EditCell(ctx, n.ID, owner+1, snapshot.Hash, n.Cells[0].ID, "x"); return err }, ErrNotebookNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
		})
	}
	if len(files.data) != 1 {
		t.Fatal("invalid operation wrote an object")
	}
}

func TestMovePreservesRelativeOrderInBothDirections(t *testing.T) {
	uc, _, _ := newTestUsecase()
	ctx := context.Background()
	n, err := uc.Create(ctx, owner, "order")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := uc.Content(ctx, n.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	a := n.Cells[0].ID
	b, err := uc.AddCell(ctx, n.ID, owner, snapshot.Hash, domain.CellKindCode, "b", nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := uc.AddCell(ctx, n.ID, owner, b.Hash, domain.CellKindCode, "c", nil)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := uc.MoveCell(ctx, n.ID, owner, c.Hash, a, 2)
	if err != nil {
		t.Fatal(err)
	}
	current, err := uc.Content(ctx, n.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if current.Cells[0].ID != b.Cell.ID || current.Cells[1].ID != c.Cell.ID || current.Cells[2].ID != a {
		t.Fatalf("bad forward move: %+v", current.Cells)
	}
	_, err = uc.MoveCell(ctx, n.ID, owner, hash, a, 0)
	if err != nil {
		t.Fatal(err)
	}
	current, err = uc.Content(ctx, n.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if current.Cells[0].ID != a || current.Cells[1].ID != b.Cell.ID || current.Cells[2].ID != c.Cell.ID {
		t.Fatalf("bad backward move: %+v", current.Cells)
	}
}
