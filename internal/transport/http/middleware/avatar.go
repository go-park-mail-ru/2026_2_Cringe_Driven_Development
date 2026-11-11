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

type avatarBodyKey struct{}

// AvatarRequestBody позволяет потоковому обработчику проверить и HTTP-эпилог.
func AvatarRequestBody(ctx context.Context) io.Reader {
	body, _ := ctx.Value(avatarBodyKey{}).(io.Reader)
	return body
}

// AvatarUpload ограничивает весь конвейер одной загрузкой и ограничивает байты запроса.
// Ставится после S2S и AuthenticateBearer, до любой валидации или биндинга тела.
func AvatarUpload(prefix string) func(http.Handler) http.Handler {
	slot := make(chan struct{}, 1)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPut || r.URL.Path != prefix+"/users/me/avatar" {
				next.ServeHTTP(w, r)
				return
			}
			if _, ok := principal.UserIDFromContext(r.Context()); !ok {
				httperr.Write(w, 401, api.ErrorCodeUnauthorized, "authentication required")
				return
			}
			contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || contentType != "multipart/form-data" {
				httperr.Write(w, 415, api.ErrorCode("unsupported_media_type"), "multipart/form-data required")
				return
			}
			if r.ContentLength > 6*1024*1024 {
				httperr.Write(w, 413, api.ErrorCode("payload_too_large"), "avatar upload exceeds size limit")
				return
			}
			select {
			case slot <- struct{}{}:
				defer func() { <-slot }()
			case <-r.Context().Done():
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 6*1024*1024)
			r = r.WithContext(context.WithValue(r.Context(), avatarBodyKey{}, io.Reader(r.Body)))
			next.ServeHTTP(w, r)
		})
	}
}
