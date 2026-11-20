package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/adapters/ipynb"
	notebookdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/delivery/http"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	usersecurity "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	"go.uber.org/mock/gomock"
)

const importFixture = `{"nbformat":4,"nbformat_minor":5,"metadata":{},"cells":[{"cell_type":"raw"},{"cell_type":"code","id":"first","source":"print(1)"}]}`

// importHTTP открывает штатный роутер и подменяет только хранилище.
func importHTTP(t *testing.T) (http.Handler, *MockRepository, string) {
	t.Helper()
	repo := NewMockRepository(gomock.NewController(t))
	tokens := usersecurity.NewAccessToken([]byte("notebook-import-secret"), time.Hour)
	access, err := tokens.Issue(testClaims(1))
	if err != nil {
		t.Fatal(err)
	}
	uc := usecase.NewNotebookUsecase(repo, usecase.NoActiveSessions{}, nil, usecase.WithImportDecoder(ipynb.DecodeImport))
	h, err := NewRouter(Server{NotebookHandler: notebookdelivery.NewHandler(uc)}, tokens, allowSessions{}, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	return h, repo, access
}

// importMultipart собирает части в указанном порядке.
func importMultipart(t *testing.T, fields ...[2]string) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, f := range fields {
		if f[0] == "file" {
			part, err := w.CreateFormFile("file", "../../arbitrary-name.txt")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(part, f[1]); err != nil {
				t.Fatal(err)
			}
		} else if err := w.WriteField(f[0], f[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), w.FormDataContentType()
}

// importRequest подготавливает запрос с сервисным ключом и пользовательским токеном.
func importRequest(body io.Reader, contentType, access string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, BaseURL+"/notebooks/import", body)
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("X-BFF-Key", "test-key")
	r.Header.Set("Authorization", "Bearer "+access)
	return r
}

func TestImportNotebookHTTP(t *testing.T) {
	for _, fileFirst := range []bool{false, true} {
		h, repo, access := importHTTP(t)
		name := [2]string{"name", "  Мой блокнот "}
		file := [2]string{"file", importFixture + strings.Repeat(" ", 600*1024)}
		fields := [][2]string{name, file}
		if fileFirst {
			fields[0], fields[1] = fields[1], fields[0]
		}
		body, ct := importMultipart(t, fields...)
		repo.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, n domain.Notebook) (domain.Notebook, error) {
			if n.Name != "Мой блокнот" || n.OwnerID != 1 || len(n.Cells) != 1 || n.Cells[0].ID != "first" {
				t.Fatalf("unexpected persisted notebook: %+v", n)
			}
			n.ID = 42
			return n, nil
		})
		r := importRequest(bytes.NewReader(body), ct, access)
		r.ContentLength = -1
		r.TransferEncoding = []string{"chunked"}
		res := serveRouter(h, r)
		var got api.ImportNotebookResponse
		if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil || res.Code != 201 || got.Notebook.Id != 42 || got.SkippedRawCells != 1 {
			t.Fatalf("status=%d body=%s error=%v", res.Code, res.Body, err)
		}
	}
}

func TestImportNotebookRejectsInvalidRequests(t *testing.T) {
	name := [2]string{"name", "Notebook"}
	file := [2]string{"file", importFixture}
	for _, tc := range []struct {
		label  string
		fields [][2]string
		adjust func(*http.Request)
		code   int
	}{
		{"missing", [][2]string{name}, nil, 400},
		{"duplicate name", [][2]string{name, name, file}, nil, 400},
		{"duplicate file", [][2]string{name, file, file}, nil, 400},
		{"extra field", [][2]string{name, file, {"extra", "x"}}, nil, 400},
		{"empty file", [][2]string{name, {"file", ""}}, nil, 400},
		{"invalid document", [][2]string{name, {"file", "{}"}}, nil, 400},
		{"oversized name before trim", [][2]string{{"name", strings.Repeat(" ", 128) + "x"}, file}, nil, 400},
		{"wrong type", nil, func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }, 415},
		{"missing boundary", nil, func(r *http.Request) { r.Header.Set("Content-Type", "multipart/form-data") }, 400},
		{"declared limit", nil, func(r *http.Request) { r.ContentLength = 129*1024*1024 + 1 }, 413},
	} {
		t.Run(tc.label, func(t *testing.T) {
			h, _, access := importHTTP(t)
			body, ct := importMultipart(t, tc.fields...)
			r := importRequest(bytes.NewReader(body), ct, access)
			if tc.adjust != nil {
				tc.adjust(r)
			}
			res := serveRouter(h, r)
			var got api.Error
			if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil || res.Code != tc.code || got.Code != api.ErrorCodeValidationError {
				t.Fatalf("status=%d body=%s error=%v", res.Code, res.Body, err)
			}
		})
	}
	h, _, access := importHTTP(t)
	body, ct := importMultipart(t, name, file)
	res := serveRouter(h, importRequest(bytes.NewReader(body[:len(body)-10]), ct, access))
	if res.Code != 400 {
		t.Fatalf("truncated request returned %d", res.Code)
	}
}

func TestImportNotebookAuthenticatesBeforeReading(t *testing.T) {
	h, _, _ := importHTTP(t)
	for _, tc := range []struct {
		key    string
		status int
	}{{"wrong", 403}, {"test-key", 401}} {
		body := &unreadBody{}
		r := importRequest(body, "multipart/form-data; boundary=x", "invalid")
		r.Header.Set("X-BFF-Key", tc.key)
		res := serveRouter(h, r)
		if res.Code != tc.status || body.reads != 0 {
			t.Fatalf("status=%d reads=%d", res.Code, body.reads)
		}
	}
}

// importPadding выдаёт байты эпилога без хранения их в памяти.
type importPadding struct{}

func (importPadding) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestImportNotebookChunkedEpilogueLimit(t *testing.T) {
	h, repo, access := importHTTP(t)
	body, ct := importMultipart(t, [2]string{"name", "Notebook"}, [2]string{"file", importFixture})
	total := int64(129 * 1024 * 1024)
	repo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.Notebook{ID: 1}, nil)
	for _, tc := range []struct {
		length int64
		status int
	}{{total, 201}, {total + 1, 413}} {
		reader := io.MultiReader(bytes.NewReader(body), io.LimitReader(importPadding{}, tc.length-int64(len(body))))
		res := serveRouter(h, importRequest(reader, ct, access))
		if res.Code != tc.status {
			t.Fatalf("total=%d status=%d body=%s", tc.length, res.Code, res.Body)
		}
	}
}
