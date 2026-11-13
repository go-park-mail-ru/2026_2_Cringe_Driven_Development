package delivery

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	"go.uber.org/mock/gomock"
)

func TestNotebookTextJSONRoundTrip(t *testing.T) {
	for _, text := range []string{
		`<script>alert(1)</script>`, `<img src=x onerror=alert(1)>`,
		`" onmouseover="alert(1)`, `A & B < C > D`, `&lt;script&gt;`, `Анализ данных`,
		"if a < b:\n    print(\"<&>\")",
	} {
		t.Run(text, func(t *testing.T) {
			now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
			notebook := domain.Notebook{ID: 1, Name: text, CreatedAt: now, UpdatedAt: now,
				Cells: []domain.Cell{{ID: "cell", Kind: domain.CellKindMarkdown, Source: text}}}
			uc := NewMockUsecase(gomock.NewController(t))
			uc.EXPECT().Create(gomock.Cond(func(ctx context.Context) bool { return ctx.Value(notebookRequestKey{}) == t.Name() }), int64(1), text).Return(notebook, nil)
			uc.EXPECT().List(gomock.Cond(func(ctx context.Context) bool { return ctx.Value(notebookRequestKey{}) == t.Name() }), int64(1), gomock.Any()).Return(usecase.ListPage{Items: []domain.NotebookSummary{{ID: 1, Name: text, UpdatedAt: now}}}, nil)
			uc.EXPECT().Get(gomock.Cond(func(ctx context.Context) bool { return ctx.Value(notebookRequestKey{}) == t.Name() }), int64(1), int64(1)).Return(notebook, nil)
			body, err := json.Marshal(api.CreateNotebookRequest{Name: text})
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range []struct {
				method, path, body string
				status             int
			}{
				{http.MethodPost, "/notebooks", string(body), http.StatusCreated},
				{http.MethodGet, "/notebooks", "", http.StatusOK},
				{http.MethodGet, "/notebooks/1", "", http.StatusOK},
			} {
				rec := do(t, uc, operation.method, operation.path, operation.body, true)
				if rec.Code != operation.status || rec.Header().Get("Content-Type") != "application/json" {
					t.Fatalf("%s %s: status=%d headers=%v body=%s", operation.method, operation.path, rec.Code, rec.Header(), rec.Body)
				}
				if operation.method == http.MethodGet && operation.path == "/notebooks" {
					var list api.NotebookListPage
					if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
						t.Fatal(err)
					}
					if len(list.Items) != 1 || list.Items[0].Name != text {
						t.Fatalf("list changed text: %+v", list)
					}
					continue
				}
				var notebook api.Notebook
				if err := json.Unmarshal(rec.Body.Bytes(), &notebook); err != nil {
					t.Fatal(err)
				}
				if notebook.Name != text || len(notebook.Cells) != 1 || notebook.Cells[0].Source != text {
					t.Fatalf("response changed text: %+v", notebook)
				}
			}

		})
	}
}

func TestValidationErrorWithHTMLIsJSON(t *testing.T) {
	rec := do(t, NewMockUsecase(gomock.NewController(t)), http.MethodPost, "/notebooks/1/cells", `{"kind":"<img src=x onerror=alert(1)>"}`, true)
	var result api.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusBadRequest || result.Code != api.ErrorCodeValidationError || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unsafe validation response: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
}
