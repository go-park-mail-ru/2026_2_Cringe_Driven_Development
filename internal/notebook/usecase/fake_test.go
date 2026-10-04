package usecase

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/models"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/repository"
)

var errS3Down = errors.New("s3 is down")

// fakeMeta хранит строки блокнотов в памяти; мьютекс заменяет блокировку строки.
type fakeMeta struct {
	mu        sync.Mutex
	notebooks map[int64]models.Notebook
	lastID    int64
}

var _ repository.Metadata = (*fakeMeta)(nil)

func (m *fakeMeta) Create(_ context.Context, n models.Notebook) (models.Notebook, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastID++
	n.ID = m.lastID
	n.CreatedAt = time.Now()
	n.UpdatedAt = n.CreatedAt
	m.notebooks[n.ID] = n
	return n, nil
}

func (m *fakeMeta) List(_ context.Context, ownerID int64) ([]models.NotebookSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := []models.NotebookSummary{}
	for _, n := range m.notebooks {
		if n.OwnerID == ownerID {
			list = append(list, models.NotebookSummary{ID: n.ID, Name: n.Name, CellsCount: n.CellsCount, UpdatedAt: n.UpdatedAt})
		}
	}
	return list, nil
}

func (m *fakeMeta) Get(_ context.Context, id, ownerID int64) (models.Notebook, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.find(id, ownerID)
}

func (m *fakeMeta) Modify(_ context.Context, id, ownerID int64, fn func(models.Notebook) (int, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, err := m.find(id, ownerID)
	if err != nil {
		return err
	}
	cellsCount, err := fn(n)
	if err != nil {
		return err
	}
	n.CellsCount = cellsCount
	n.UpdatedAt = time.Now()
	m.notebooks[id] = n
	return nil
}

func (m *fakeMeta) find(id, ownerID int64) (models.Notebook, error) {
	n, ok := m.notebooks[id]
	if !ok || n.OwnerID != ownerID {
		return models.Notebook{}, repository.ErrNotebookNotFound
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

var _ repository.Files = (*fakeFiles)(nil)

func (f *fakeFiles) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.data[key]
	if !ok {
		return nil, repository.ErrFileNotFound
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
	meta := &fakeMeta{notebooks: map[int64]models.Notebook{}}
	files := &fakeFiles{data: map[string][]byte{}}
	uc := NewNotebookUsecase(meta, files)
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
