package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/httperr"
)

const MaxRequestBodyBytes = 512 * 1024

// BodyLimit проверяет поток до JSON-декодера и OpenAPI-валидации,
// включая запросы без Content-Length и с chunked encoding.
func BodyLimit(next http.Handler, maxSourceBytes int, prefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != prefix && !strings.HasPrefix(r.URL.Path, prefix+"/") {
			next.ServeHTTP(w, r)
			return
		}
		if r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > MaxRequestBodyBytes {
			httperr.Write(w, http.StatusRequestEntityTooLarge, api.ValidationError, "request body too large")
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes))
		_ = r.Body.Close()
		if err != nil {
			httperr.RequestError(w, r, err)
			return
		}
		// Ограничение decoded UTF-8 source проверяется до OpenAPI, где
		// maxLength измеряется в символах, а не в байтах.
		var body struct {
			Source string `json:"source"`
		}
		if json.Unmarshal(data, &body) == nil && len(body.Source) > maxSourceBytes {
			httperr.Write(w, http.StatusRequestEntityTooLarge, api.ValidationError, "cell source too large")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
		next.ServeHTTP(w, r)
	})
}
