package middleware

import (
	"net/http"
	"strings"
)

// NotebookCachePolicy запрещает кэшировать содержимое и права, включая ошибки
// до delivery (например, отказ S2S или аутентификации).
func NotebookCachePolicy(prefix string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, prefix+"/notebooks/") {
				parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix+"/notebooks/"), "/")
				read := r.Method == http.MethodGet && (len(parts) == 1 || (len(parts) == 2 && (parts[1] == "content" || parts[1] == "sharing")))
				sharing := r.Method == http.MethodPut && len(parts) == 2 && parts[1] == "sharing"
				if read || sharing {
					w.Header().Set("Cache-Control", "no-store")
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
