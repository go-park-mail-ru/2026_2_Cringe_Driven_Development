package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

func TestOversizedSourceDoesNotPublishRevision(t *testing.T) {
	for _, operation := range []string{"add", "edit"} {
		t.Run(operation, func(t *testing.T) {
			uc, meta, files := newTestUsecase()
			ctx := context.Background()
			n, err := uc.Create(ctx, owner, "limits")
			if err != nil {
				t.Fatal(err)
			}
			before, err := uc.Content(ctx, n.ID, owner)
			if err != nil {
				t.Fatal(err)
			}
			source := strings.Repeat("x", 65537)
			if operation == "add" {
				_, err = uc.AddCell(ctx, n.ID, owner, before.Hash, domain.CellKindCode, source, nil)
			} else {
				_, err = uc.EditCell(ctx, n.ID, owner, before.Hash, before.Cells[0].ID, source)
			}
			if err == nil {
				t.Fatal("oversized source was accepted")
			}
			if meta.notebooks[n.ID].FileKey != n.FileKey || len(files.data) != 1 {
				t.Fatal("oversized edit published a revision")
			}
		})
	}
}

func TestOversizedStoredNotebookIsRejected(t *testing.T) {
	uc, _, files := newTestUsecase()
	ctx := context.Background()
	n, err := uc.Create(ctx, owner, "large")
	if err != nil {
		t.Fatal(err)
	}
	files.data[n.FileKey] = append(files.data[n.FileKey], []byte(strings.Repeat(" ", 128*1024*1024))...)
	if _, err = uc.Content(ctx, n.ID, owner); err == nil {
		t.Fatal("oversized notebook was parsed")
	}
	if _, err = uc.Get(ctx, n.ID, owner); err == nil {
		t.Fatal("oversized notebook was read")
	}
}

type oversizedRewrite struct{ NotebookCodec }

func (c oversizedRewrite) Rewrite([]byte, []domain.Cell) ([]byte, error) {
	return make([]byte, 128*1024*1024+1), nil
}

func TestOversizedSerializedRevisionIsNotPublished(t *testing.T) {
	uc, meta, files := newTestUsecase()
	ctx := context.Background()
	n, err := uc.Create(ctx, owner, "output limit")
	if err != nil {
		t.Fatal(err)
	}
	before, err := uc.Content(ctx, n.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	uc.codec = oversizedRewrite{uc.codec}
	_, err = uc.EditCell(ctx, n.ID, owner, before.Hash, n.Cells[0].ID, "changed")
	if err == nil {
		t.Fatal("oversized serialized revision accepted")
	}
	if meta.notebooks[n.ID].FileKey != n.FileKey || len(files.data) != 1 {
		t.Fatal("oversized object was uploaded")
	}
}

func TestLegacyIndexMutationRejectsStaleIntent(t *testing.T) {
	uc, _, files := newTestUsecase()
	ctx := context.Background()
	n, err := uc.Create(ctx, owner, "two tabs")
	if err != nil {
		t.Fatal(err)
	}
	a, err := uc.Content(ctx, n.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	added, err := uc.AddCell(ctx, n.ID, owner, a.Hash, domain.CellKindMarkdown, "B", nil)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := uc.MoveCell(ctx, n.ID, owner, added.Hash, added.Cell.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := len(files.data)
	if err = uc.DeleteCell(ctx, n.ID, owner, added.Hash, 0); !errors.Is(err, ErrNotebookConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	if _, err = uc.CreateCell(ctx, n.ID, owner, added.Hash, domain.CellKindCode, nil); !errors.Is(err, ErrNotebookConflict) {
		t.Fatalf("stale insert: %v", err)
	}
	if len(files.data) != count {
		t.Fatal("stale mutation uploaded a revision")
	}
	now, err := uc.Content(ctx, n.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if now.Hash != moved || len(now.Cells) != 2 || now.Cells[0].ID != added.Cell.ID {
		t.Fatal("stale intent modified another cell")
	}
	if err = uc.DeleteCell(ctx, n.ID, owner, moved, 0); err != nil {
		t.Fatal(err)
	}
	now, err = uc.Content(ctx, n.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(now.Cells) != 1 || now.Cells[0].ID != n.Cells[0].ID {
		t.Fatal("valid index delete removed the wrong cell")
	}
}

func TestSourceAtByteLimitIsAccepted(t *testing.T) {
	for _, source := range []string{strings.Repeat("x", 65536), strings.Repeat("я", 32768)} {
		uc, _, _ := newTestUsecase()
		ctx := context.Background()
		n, err := uc.Create(ctx, owner, "boundary")
		if err != nil {
			t.Fatal(err)
		}
		added, err := uc.AddCell(ctx, n.ID, owner, testRevision(t, uc, n.ID, owner), domain.CellKindMarkdown, source, nil)
		if err != nil {
			t.Fatal(err)
		}
		changed, err := uc.EditCell(ctx, n.ID, owner, added.Hash, added.Cell.ID, source)
		if err != nil || changed.Cell.Source != source {
			t.Fatalf("boundary rejected: %v", err)
		}
	}
}
