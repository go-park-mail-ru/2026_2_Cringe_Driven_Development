package usecase

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/observability"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/middleware"
)

type loggingFiles struct{ base *fakeFiles }

func (f loggingFiles) Get(ctx context.Context, key string) ([]byte, error) {
	started := time.Now()
	data, err := f.base.Get(ctx, key)
	observability.LogStorage(ctx, "notebook.get_object", started, err)
	return data, err
}

func (f loggingFiles) Put(ctx context.Context, key string, data []byte) error {
	started := time.Now()
	err := f.base.Put(ctx, key, data)
	observability.LogStorage(ctx, "notebook.put_object", started, err)
	return err
}

func TestNotebookChangesLogOnlySuccessfulEvents(t *testing.T) {
	var out bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(observability.NewHandler(slog.NewJSONHandler(&out, nil))))
	t.Cleanup(func() { slog.SetDefault(old) })
	ctx := observability.WithRequestID(context.Background(), "req-notebook")
	uc, _, files := newTestUsecase()
	n, err := uc.Create(ctx, owner, "SECRET-name")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uc.CreateCell(ctx, n.ID, owner, testRevision(t, uc, n.ID, owner), domain.CellKindCode, nil); err != nil {
		t.Fatal(err)
	}
	if err := uc.DeleteCell(ctx, n.ID, owner, testRevision(t, uc, n.ID, owner), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.Get(ctx, n.ID, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.List(ctx, owner); err != nil {
		t.Fatal(err)
	}
	files.down = true
	if _, err := uc.Create(ctx, owner, "failed"); err == nil {
		t.Fatal("expected S3 failure")
	}
	log := out.String()
	for _, event := range []string{"notebook.create", "notebook.cell_create", "notebook.cell_delete"} {
		if strings.Count(log, `"msg":"`+event+`"`) != 1 {
			t.Fatalf("event %s: %s", event, log)
		}
	}
	if strings.Count(log, `"request_id":"req-notebook"`) != 3 || strings.Contains(log, "SECRET") || strings.Contains(log, n.FileKey) {
		t.Fatalf("unsafe logs: %s", log)
	}
}

func TestRequestIDCorrelatesHTTPUsecaseAndStorage(t *testing.T) {
	var out bytes.Buffer
	var level slog.LevelVar
	level.Set(slog.LevelDebug)
	old := slog.Default()
	slog.SetDefault(slog.New(observability.NewHandler(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: &level}))))
	t.Cleanup(func() { slog.SetDefault(old) })
	uc, _, files := newTestUsecase()
	uc.files = loggingFiles{files}
	handler := middleware.RequestID(middleware.Logging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := uc.Create(r.Context(), owner, "safe"); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
	})))
	request := func() *httptest.ResponseRecorder {
		out.Reset()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/notebooks", nil))
		return w
	}
	w := request()
	log := out.String()
	if w.Code != 201 || strings.Count(log, w.Header().Get("X-Request-ID")) != 3 || !strings.Contains(log, `"msg":"http.request"`) || !strings.Contains(log, `"msg":"notebook.create"`) || !strings.Contains(log, `"operation":"notebook.put_object"`) {
		t.Fatalf("uncorrelated debug logs: %s", log)
	}
	level.Set(slog.LevelInfo)
	w = request()
	log = out.String()
	if w.Code != 201 || strings.Count(log, w.Header().Get("X-Request-ID")) != 2 || strings.Contains(log, `"operation":"notebook.put_object"`) {
		t.Fatalf("wrong info logs: %s", log)
	}
}
