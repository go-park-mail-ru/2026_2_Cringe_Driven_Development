package usecase

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/ipynb"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

var errS3Down = errors.New("s3 is down")

// Stateful fixtures support the create/edit/read and logging flows.
// Their mutex protects fixture maps; it does not prove database atomicity.
type fakeMeta struct {
	mu        sync.Mutex
	notebooks map[int64]domain.Notebook
	lastID    int64
	modifyErr error
}

func (m *fakeMeta) Create(_ context.Context, n domain.Notebook) (domain.Notebook, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastID++
	n.ID = m.lastID
	n.CreatedAt = time.Now()
	n.UpdatedAt = n.CreatedAt
	m.notebooks[n.ID] = n
	return n, nil
}

func (m *fakeMeta) List(_ context.Context, ownerID int64) ([]domain.NotebookSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := []domain.NotebookSummary{}
	for _, n := range m.notebooks {
		if n.OwnerID == ownerID {
			list = append(list, domain.NotebookSummary{ID: n.ID, Name: n.Name, CellsCount: n.CellsCount, UpdatedAt: n.UpdatedAt})
		}
	}
	return list, nil
}

func (m *fakeMeta) Get(_ context.Context, id, ownerID int64) (domain.Notebook, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.find(id, ownerID)
}

func (m *fakeMeta) Modify(_ context.Context, id, ownerID int64, fn func(domain.Notebook) (MetadataUpdate, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, err := m.find(id, ownerID)
	if err != nil {
		return err
	}
	update, err := fn(n)
	if err != nil {
		return err
	}
	if m.modifyErr != nil {
		return m.modifyErr
	}
	if !update.Changed {
		return nil
	}
	n.FileKey = update.FileKey
	n.CellsCount = update.CellsCount
	n.UpdatedAt = time.Now()
	m.notebooks[id] = n
	return nil
}

func (m *fakeMeta) find(id, ownerID int64) (domain.Notebook, error) {
	n, ok := m.notebooks[id]
	if !ok || n.OwnerID != ownerID {
		return domain.Notebook{}, ErrNotebookNotFound
	}
	return n, nil
}

// fakeFiles хранит файлы в map вместо S3.
type fakeFiles struct {
	mu   sync.Mutex
	data map[string][]byte
	// down включает ошибку на Put, как будто S3 недоступен.
	down bool
}

func (f *fakeFiles) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.data[key]
	if !ok {
		return nil, ErrFileNotFound
	}
	return data, nil
}

func (f *fakeFiles) Put(_ context.Context, key string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return errS3Down
	}
	f.data[key] = data
	return nil
}

// newTestUsecase выдаёт id по порядку: c1, c2, c3...
func newTestUsecase() (*NotebookUsecase, *fakeMeta, *fakeFiles) {
	meta := &fakeMeta{notebooks: map[int64]domain.Notebook{}}
	files := &fakeFiles{data: map[string][]byte{}}
	uc := NewNotebookUsecase(meta, files, ipynb.Codec{})
	var mu sync.Mutex
	next := 0
	uc.newID = func() string {
		mu.Lock()
		defer mu.Unlock()
		next++
		return "c" + strconv.Itoa(next)
	}
	return uc, meta, files
}

func testRevision(t *testing.T, uc *NotebookUsecase, id, ownerID int64) string {
	t.Helper()
	snapshot, err := uc.Content(context.Background(), id, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Hash
}
