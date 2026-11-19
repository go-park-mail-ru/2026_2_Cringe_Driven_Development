package delivery

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	F "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/principal"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// fileUsecase подставляет поток и проверяет допущенные параметры файлового входа.
type fileUsecase struct {
	Usecase
	response F.FileResponse
	err      error
	called   bool
	owner    int64
	request  F.FileRequest
}

// Files фиксирует запрос и возвращает подготовленный поток.
func (u *fileUsecase) Files(_ context.Context, owner int64, _ uuid.UUID, r F.FileRequest) (F.FileResponse, error) {
	u.called = true
	u.owner = owner
	u.request = r
	return u.response, u.err
}

// fileRequest создаёт запрос после Bearer middleware с параметрами mux.
func fileRequest(method, tail, id string, ownerID int64) *http.Request {
	req := httptest.NewRequest(method, "http://backend/files?offset=2147483648", strings.NewReader("body"))
	req = mux.SetURLVars(req, map[string]string{"id": id, "file_path": tail})
	req = req.WithContext(principal.WithUserID(req.Context(), ownerID))
	req.Header.Set("Range", "bytes=2147483648-")
	req.Header.Set("If-Range", `"version"`)
	req.Header.Set("X-Workspace-Socket", "socket")
	return req
}

// TestWorkspaceHTTPBoundary проверяет допуск владельца, варианты маршрутов и bounded ответы.
func TestWorkspaceHTTPBoundary(t *testing.T) {
	id, tid := uuid.NewString(), uuid.NewString()
	for _, tc := range []struct {
		name, method, tail, id string
		owner                  int64
		err                    error
		status                 int
	}{
		{"command", "POST", "command", id, 7, nil, 206},
		{"events", "GET", "events", id, 7, nil, 206},
		{"check", "GET", "check", id, 7, nil, 206},
		{"disconnect", "DELETE", "socket", id, 7, nil, 206},
		{"chunk", "POST", "transfers/" + tid + "/chunks", id, 7, nil, 206},
		{"download", "GET", "transfers/" + tid + "/download", id, 7, nil, 206},
		{"foreign", "POST", "command", id, 7, D.ErrNotFound, 404},
		{"unavailable", "POST", "command", id, 7, D.ErrUnavailable, 503},
		{"closed", "POST", "command", id, 7, D.ErrWorkspaceClosed, 410},
		{"guest", "POST", "command", id, 0, nil, 401},
		{"bad ID", "POST", "command", "bad", 7, nil, 400},
		{"bad transfer", "GET", "transfers/bad/download", id, 7, nil, 404},
		{"extra path", "GET", "transfers/" + tid + "/extra/download", id, 7, nil, 404},
		{"wrong method", "GET", "command", id, 7, nil, 404},
		{"unknown", "GET", "other", id, 7, nil, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uc := &fileUsecase{response: F.FileResponse{Status: 206, Headers: map[string]string{"Content-Range": "bytes 2147483648-2147483651/2147483652"}, Body: io.NopCloser(strings.NewReader("tail"))}, err: tc.err}
			writer := httptest.NewRecorder()
			NewHandler(uc).Workspace(writer, fileRequest(tc.method, tc.tail, tc.id, tc.owner))
			if writer.Code != tc.status {
				t.Fatalf("status=%d body=%s", writer.Code, writer.Body.String())
			}
			if tc.status == 206 {
				if uc.owner != 7 || uc.request.Path != "/workspace/"+tc.tail || uc.request.Headers["Range"] != "bytes=2147483648-" || writer.Body.String() != "tail" || writer.Header().Get("Content-Range") == "" {
					t.Fatalf("request=%+v body=%s", uc.request, writer.Body.String())
				}
			}
		})
	}
}

// TestWorkspaceRejectsBodyBeforeAgent проверяет лимиты JSON и бинарной части.
func TestWorkspaceRejectsBodyBeforeAgent(t *testing.T) {
	for _, tail := range []string{"command", "transfers/" + uuid.NewString() + "/chunks"} {
		uc := &fileUsecase{}
		request := fileRequest("POST", tail, uuid.NewString(), 7)
		request.ContentLength = 2 << 20
		writer := httptest.NewRecorder()
		NewHandler(uc).Workspace(writer, request)
		if writer.Code != 413 || uc.called {
			t.Fatalf("status=%d called=%v", writer.Code, uc.called)
		}
	}
	writer := httptest.NewRecorder()
	NewHandler(&testUsecase{}).Workspace(writer, fileRequest("POST", "command", uuid.NewString(), 7))
	if writer.Code != 503 {
		t.Fatal(writer.Code)
	}
}

// failingWriter моделирует отменённого или медленного consumer.
type failingWriter struct {
	*httptest.ResponseRecorder
	deadline bool
	write    bool
	flush    bool
}

// SetWriteDeadline возвращает заданный отказ установки deadline.
func (w failingWriter) SetWriteDeadline(_ time.Time) error {
	if w.deadline {
		return errors.New("deadline unavailable")
	}
	return nil
}

// Write моделирует закрытие браузерного соединения.
func (w failingWriter) Write(b []byte) (int, error) {
	if w.write {
		return 0, errors.New("closed")
	}
	return w.ResponseRecorder.Write(b)
}

// FlushError моделирует отказ отправки буфера HTTP.
func (w failingWriter) FlushError() error {
	if w.flush {
		return errors.New("closed")
	}
	return nil
}

// TestWorkspaceConsumerFailureClosesBody проверяет освобождение ответа при отказе consumer.
func TestWorkspaceConsumerFailureClosesBody(t *testing.T) {
	for _, kind := range []string{"deadline", "write", "flush"} {
		body := &trackedBody{Reader: strings.NewReader("payload")}
		uc := &fileUsecase{response: F.FileResponse{Status: 200, Body: body}}
		writer := failingWriter{httptest.NewRecorder(), kind == "deadline", kind == "write", kind == "flush"}
		NewHandler(uc).Workspace(writer, fileRequest("POST", "command", uuid.NewString(), 7))
		if kind != "deadline" && !body.closed {
			t.Fatal("body leaked")
		}
	}
}

// trackedBody фиксирует владение потоком.
type trackedBody struct {
	*strings.Reader
	closed bool
}

// Close фиксирует освобождение ресурса.
func (b *trackedBody) Close() error { b.closed = true; return nil }
