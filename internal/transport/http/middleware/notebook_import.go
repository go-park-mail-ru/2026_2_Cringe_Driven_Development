package middleware

import (
	"context"
	"io"
	"mime"
	"net/http"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/httperr"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/principal"
)

const notebookImportPath = "/notebooks/import"
const maxNotebookImportBody = 129 * 1024 * 1024

// notebookImportBodyKey связывает обработчик с ограниченным HTTP-потоком.
type notebookImportBodyKey struct{}

// NotebookImportBody возвращает поток для дочитывания HTTP-эпилога.
func NotebookImportBody(ctx context.Context) io.Reader {
	body, _ := ctx.Value(notebookImportBodyKey{}).(io.Reader)
	return body
}

// NotebookImport ограничивает тело импорта после S2S и Bearer.
func NotebookImport(prefix string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != prefix+notebookImportPath {
				next.ServeHTTP(w, r)
				return
			}
			if _, ok := principal.UserIDFromContext(r.Context()); !ok {
				httperr.Write(w, http.StatusUnauthorized, api.ErrorCodeUnauthorized, "authentication required")
				return
			}
			if r.ContentLength > maxNotebookImportBody {
				httperr.Write(w, http.StatusRequestEntityTooLarge, api.ErrorCodeValidationError, "notebook import exceeds size limit")
				return
			}
			contentType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if contentType != "multipart/form-data" {
				httperr.Write(w, http.StatusUnsupportedMediaType, api.ErrorCodeValidationError, "multipart/form-data required")
				return
			}
			if err != nil || params["boundary"] == "" {
				httperr.Write(w, http.StatusBadRequest, api.ErrorCodeValidationError, "invalid multipart boundary")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxNotebookImportBody)
			r = r.WithContext(context.WithValue(r.Context(), notebookImportBodyKey{}, io.Reader(r.Body)))
			next.ServeHTTP(w, r)
		})
	}
}
