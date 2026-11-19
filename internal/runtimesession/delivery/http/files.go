package delivery

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	F "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/usecase"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Workspace обслуживает внутренний файловый маршрут после S2S и Bearer middleware.
func (h *Handler) Workspace(w http.ResponseWriter, r *http.Request) {
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(60 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return
	}
	if owner(r.Context()) <= 0 {
		http.Error(w, `{"code":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		http.Error(w, `{"code":"invalid_path"}`, http.StatusBadRequest)
		return
	}
	tail := mux.Vars(r)["file_path"]
	valid := (tail == "check" && r.Method == http.MethodGet) || (tail == "command" && r.Method == http.MethodPost) || (tail == "events" && r.Method == http.MethodGet) || (tail == "socket" && r.Method == http.MethodDelete)
	if strings.HasPrefix(tail, "transfers/") {
		parts := strings.Split(tail, "/")
		if len(parts) == 3 {
			_, err = uuid.Parse(parts[1])
			valid = err == nil && ((parts[2] == "chunks" && r.Method == http.MethodPost) || (parts[2] == "download" && r.Method == http.MethodGet))
		}
	}
	if !valid {
		http.NotFound(w, r)
		return
	}
	limit := int64(65536)
	if strings.HasSuffix(tail, "/chunks") {
		limit = 1 << 20
	}
	if r.ContentLength > limit {
		http.Error(w, "file message too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	uc, ok := h.uc.(interface {
		Files(context.Context, int64, uuid.UUID, F.FileRequest) (F.FileResponse, error)
	})
	if !ok {
		http.Error(w, `{"code":"runtime_not_ready"}`, http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	response, err := uc.Files(ctx, owner(ctx), id, F.FileRequest{
		Method: r.Method, Path: "/workspace/" + tail, Query: r.URL.RawQuery, Length: r.ContentLength, Body: r.Body,
		Headers: map[string]string{"Content-Type": r.Header.Get("Content-Type"), "Range": r.Header.Get("Range"), "If-Range": r.Header.Get("If-Range"), "X-Workspace-Socket": r.Header.Get("X-Workspace-Socket")},
	})
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, D.ErrWorkspaceClosed) {
			http.Error(w, `{"code":"workspace_closed"}`, http.StatusGone)
			return
		}
		if errors.Is(err, D.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, `{"code":"runtime_not_ready"}`, status)
		return
	}
	defer response.Body.Close()
	stall := time.AfterFunc(60*time.Second, cancel)
	defer stall.Stop()
	for key, value := range response.Headers {
		w.Header().Set(key, value)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(response.Status)
	// Read/write deadline обновляется после каждого блока, поэтому медленная активная
	// передача может продолжаться дольше общего idle-timeout runtime.
	buffer := make([]byte, 64<<10)
	for {
		if err := controller.SetWriteDeadline(time.Now().Add(60 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return
		}
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			stall.Reset(60 * time.Second)
			if _, err := w.Write(buffer[:n]); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
		if readErr != nil {
			return
		}
	}
}
