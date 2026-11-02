package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/google/go-cmp/cmp"
	"go.uber.org/mock/gomock"
)

func TestCreateErrors(t *testing.T) {
	for _, stage := range []string{"empty", "codec", "file", "metadata"} {
		t.Run(stage, func(t *testing.T) {
			f := newNotebookFixture(t)
			ctx := context.Background()
			source := errors.New("failed " + stage)
			want := source
			name := "name"
			if stage == "empty" {
				name = "   "
				want = ErrEmptyName
			} else {
				build := f.codec.EXPECT().Build([]domain.Cell{{ID: "fixed-id", Kind: domain.CellKindCode}})
				if stage == "codec" {
					build.Return(nil, source)
				} else {
					build.Return([]byte("file"), nil)
					put := f.files.EXPECT().Put(ctx, "notebooks/fixed-id.ipynb", []byte("file")).After(build)
					if stage == "file" {
						put.Return(source)
					} else {
						put.Return(nil)
						f.meta.EXPECT().Create(ctx, domain.Notebook{OwnerID: owner, Name: name, FileKey: "notebooks/fixed-id.ipynb", CellsCount: 1}).After(put).Return(domain.Notebook{}, source)
					}
				}
			}
			_, err := f.uc.Create(ctx, owner, name)
			if !errors.Is(err, want) {
				t.Fatalf("err=%v want=%v", err, want)
			}
		})
	}
}

func TestListAndGet(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		f := newNotebookFixture(t)
		ctx := context.Background()
		want := []domain.NotebookSummary{{ID: 7, Name: "name"}}
		f.meta.EXPECT().List(ctx, owner).Return(want, nil)
		got, err := f.uc.List(ctx, owner)
		if err != nil || !cmp.Equal(want, got) {
			t.Fatalf("got=%v err=%v", got, err)
		}
	})
	for _, stage := range []string{"metadata", "file", "parse", "success"} {
		t.Run(stage, func(t *testing.T) {
			f := newNotebookFixture(t)
			ctx := context.Background()
			source := errors.New(stage + " failed")
			n := domain.Notebook{ID: 7, OwnerID: owner, FileKey: "key"}
			cells := []domain.Cell{{ID: "c1", Kind: domain.CellKindCode, Source: "print(1)"}}
			meta := f.meta.EXPECT().Get(ctx, int64(7), owner)
			if stage == "metadata" {
				meta.Return(domain.Notebook{}, source)
			} else {
				meta.Return(n, nil)
				get := f.files.EXPECT().Get(ctx, "key").After(meta)
				if stage == "file" {
					get.Return(nil, source)
				} else {
					get.Return([]byte("file"), nil)
					parse := f.codec.EXPECT().Parse([]byte("file")).After(get)
					if stage == "parse" {
						parse.Return(nil, source)
					} else {
						parse.Return(cells, nil)
					}
				}
			}
			got, err := f.uc.Get(ctx, 7, owner)
			if stage == "success" {
				n.Cells = cells
				if err != nil || !cmp.Equal(got, n) {
					t.Fatalf("got=%v err=%v", got, err)
				}
			} else if !errors.Is(err, source) {
				t.Fatal(err)
			}
		})
	}
}

func TestNotFound(t *testing.T) {
	for _, op := range []string{"get", "create cell", "delete cell"} {
		for _, idOwner := range [][2]int64{{7, owner + 1}, {999, owner}} {
			t.Run(op+"/"+string(rune(idOwner[1]+'0')), func(t *testing.T) {
				f := newNotebookFixture(t)
				ctx := context.Background()
				var err error
				if op == "get" {
					f.meta.EXPECT().Get(ctx, idOwner[0], idOwner[1]).Return(domain.Notebook{}, ErrNotebookNotFound)
					_, err = f.uc.Get(ctx, idOwner[0], idOwner[1])
				} else {
					f.meta.EXPECT().Modify(ctx, idOwner[0], idOwner[1], gomock.Any()).Return(ErrNotebookNotFound)
					if op == "create cell" {
						_, err = f.uc.CreateCell(ctx, idOwner[0], idOwner[1], contentHash([]byte("input")), domain.CellKindCode, nil)
					} else {
						err = f.uc.DeleteCell(ctx, idOwner[0], idOwner[1], contentHash([]byte("input")), 0)
					}
				}
				if !errors.Is(err, ErrNotebookNotFound) {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestModifyCellCallback(t *testing.T) {
	idx := func(i int) *int { return &i }
	for _, tc := range []struct {
		name    string
		remove  bool
		index   *int
		cells   []domain.Cell
		want    []domain.Cell
		wantErr error
	}{
		{name: "append", cells: []domain.Cell{{ID: "c1", Kind: domain.CellKindCode}}, want: []domain.Cell{{ID: "c1", Kind: domain.CellKindCode}, {ID: "fixed-id", Kind: domain.CellKindMarkdown}}},
		{name: "prepend", index: idx(0), cells: []domain.Cell{{ID: "c1", Kind: domain.CellKindCode}}, want: []domain.Cell{{ID: "fixed-id", Kind: domain.CellKindMarkdown}, {ID: "c1", Kind: domain.CellKindCode}}},
		{name: "empty append", cells: []domain.Cell{}, want: []domain.Cell{{ID: "fixed-id", Kind: domain.CellKindMarkdown}}},
		{name: "negative", index: idx(-1), cells: []domain.Cell{}, wantErr: ErrIndexOutOfRange},
		{name: "past end", index: idx(2), cells: []domain.Cell{}, wantErr: ErrIndexOutOfRange},
		{name: "limit", cells: make([]domain.Cell, MaxCells), wantErr: ErrTooManyCells},
		{name: "delete last", remove: true, index: idx(0), cells: []domain.Cell{{ID: "c1", Kind: domain.CellKindCode}}, want: []domain.Cell{}},
		{name: "delete empty", remove: true, index: idx(0), cells: []domain.Cell{}, wantErr: ErrCellNotFound},
		{name: "delete negative", remove: true, index: idx(-1), cells: []domain.Cell{}, wantErr: ErrCellNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNotebookFixture(t)
			ctx := context.Background()
			called := false
			f.meta.EXPECT().Modify(ctx, int64(7), owner, gomock.Any()).DoAndReturn(func(_ context.Context, _ int64, _ int64, fn func(domain.Notebook) (MetadataUpdate, error)) error {
				called = true
				update, err := fn(domain.Notebook{ID: 7, OwnerID: owner, FileKey: "key", CellsCount: len(tc.cells)})
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("callback error=%v want=%v", err, tc.wantErr)
				}
				if err == nil && (update.CellsCount != len(tc.want) || !update.Changed || update.FileKey == "key" || update.FileKey == "") {
					t.Fatalf("callback count=%d want=%d", update.CellsCount, len(tc.want))
				}
				return err
			})
			gomock.InOrder(f.files.EXPECT().Get(ctx, "key").Return([]byte("input"), nil), f.codec.EXPECT().Parse([]byte("input")).Return(tc.cells, nil))
			if tc.wantErr == nil {
				gomock.InOrder(f.codec.EXPECT().Rewrite([]byte("input"), tc.want).Return([]byte("output"), nil), f.files.EXPECT().Put(ctx, gomock.Cond(func(k string) bool {
					return strings.HasPrefix(k, "notebooks/") && strings.HasSuffix(k, ".ipynb") && k != "key"
				}), []byte("output")).Return(nil))
			}
			var err error
			if tc.remove {
				err = f.uc.DeleteCell(ctx, 7, owner, contentHash([]byte("input")), *tc.index)
			} else {
				var cell domain.Cell
				cell, err = f.uc.CreateCell(ctx, 7, owner, contentHash([]byte("input")), domain.CellKindMarkdown, tc.index)
				if tc.wantErr == nil && cell != (domain.Cell{ID: "fixed-id", Kind: domain.CellKindMarkdown}) {
					t.Fatalf("cell=%v", cell)
				}
			}
			if !called || !errors.Is(err, tc.wantErr) {
				t.Fatalf("called=%v err=%v", called, err)
			}
		})
	}
}

func TestModifyStorageFailures(t *testing.T) {
	for _, stage := range []string{"get", "parse", "build", "put"} {
		t.Run(stage, func(t *testing.T) {
			f := newNotebookFixture(t)
			ctx := context.Background()
			source := errors.New(stage + " failed")
			f.meta.EXPECT().Modify(ctx, int64(7), owner, gomock.Any()).DoAndReturn(func(_ context.Context, _ int64, _ int64, fn func(domain.Notebook) (MetadataUpdate, error)) error {
				_, err := fn(domain.Notebook{ID: 7, FileKey: "key", CellsCount: 1})
				if !errors.Is(err, source) {
					t.Fatalf("callback err=%v", err)
				}
				return err
			})
			get := f.files.EXPECT().Get(ctx, "key")
			if stage == "get" {
				get.Return(nil, source)
			} else {
				get.Return([]byte("input"), nil)
				parse := f.codec.EXPECT().Parse([]byte("input"))
				if stage == "parse" {
					parse.Return(nil, source)
				} else {
					parse.Return([]domain.Cell{}, nil)
					build := f.codec.EXPECT().Rewrite([]byte("input"), []domain.Cell{{ID: "fixed-id", Kind: domain.CellKindCode}})
					if stage == "build" {
						build.Return(nil, source)
					} else {
						build.Return([]byte("output"), nil)
						f.files.EXPECT().Put(ctx, gomock.Cond(func(k string) bool {
							return strings.HasPrefix(k, "notebooks/") && strings.HasSuffix(k, ".ipynb") && k != "key"
						}), []byte("output")).Return(source)
					}
				}
			}
			_, err := f.uc.CreateCell(ctx, 7, owner, contentHash([]byte("input")), domain.CellKindCode, nil)
			if !errors.Is(err, source) {
				t.Fatal(err)
			}
		})
	}
}
