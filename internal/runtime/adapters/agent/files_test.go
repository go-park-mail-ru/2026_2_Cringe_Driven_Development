package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	U "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	F "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/usecase"
)

// TestFilesPreservesStreamingRangeAndRequestID проверяет защищённую передачу бинарного тела.
func TestFilesPreservesStreamingRangeAndRequestID(t *testing.T) {
	ca := newCA(t, "files-ca")
	server, state := startServer(t, ca, ca, true, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=2147483648-" {
			t.Error("range lost")
		}
		if r.URL.Path != "/workspace/transfers/file/download" {
			t.Error(r.URL.Path)
		}
		w.Header().Set("Content-Disposition", `attachment; filename="file"`)
		w.Header().Set("Content-Range", "bytes 2147483648-2147483651/2147483652")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.Copy(w, strings.NewReader("tail"))
	})
	response, err := newClient(t, ca).Files(context.Background(), U.AgentEndpoint{BaseURL: server.URL, Token: tok}, F.FileRequest{
		Method: http.MethodGet, Path: "/workspace/transfers/file/download", Headers: map[string]string{"Range": "bytes=2147483648-"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "tail" || response.Status != 206 || state.auth != "Bearer "+tok || state.peers != 1 {
		t.Fatalf("response=%+v err=%v body=%q", response, err, body)
	}
}

// TestFilesRejectsInvalidPathAndPropagatesCancellation проверяет отказ до сети и отмену запроса.
func TestFilesRejectsInvalidPathAndPropagatesCancellation(t *testing.T) {
	client := &Client{http: &http.Client{}}
	if _, err := client.Files(context.Background(), U.AgentEndpoint{}, F.FileRequest{Path: "/outside"}); err == nil {
		t.Fatal("outside path admitted")
	}
	if _, err := client.Files(context.Background(), U.AgentEndpoint{BaseURL: "://invalid"}, F.FileRequest{Method: "bad method", Path: "/workspace/check"}); err == nil {
		t.Fatal("bad method admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Files(ctx, U.AgentEndpoint{BaseURL: "http://127.0.0.1:1"}, F.FileRequest{Method: "GET", Path: "/workspace/check"}); err == nil {
		t.Fatal("cancelled request admitted")
	}
}

// TestFilesServiceCredentialFailureDoesNotRevokeBrowserSession отличает отказ агента от пользовательского 401.
func TestFilesServiceCredentialFailureDoesNotRevokeBrowserSession(t *testing.T) {
	ca := newCA(t, "file-auth-ca")
	server, _ := startServer(t, ca, ca, true, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	_, err := newClient(t, ca).Files(context.Background(), U.AgentEndpoint{BaseURL: server.URL, Token: tok}, F.FileRequest{Method: "GET", Path: "/workspace/check"})
	if !errors.Is(err, D.ErrUnavailable) {
		t.Fatal(err)
	}
}
