package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/principal"
)

func TestAvatarSinglePipelineAndCancelledWaiter(t *testing.T) {
	entered, release, done := make(chan struct{}, 2), make(chan struct{}), make(chan struct{})
	h := AvatarUpload("/api/v1")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		if r.Header.Get("First") == "true" {
			<-release
		}
		w.WriteHeader(204)
	}))
	request := func(ctx context.Context) *http.Request {
		r := httptest.NewRequest("PUT", "/api/v1/users/me/avatar", nil)
		r = r.WithContext(principal.WithUserID(ctx, 42))
		r.Header.Set("Content-Type", "multipart/form-data; boundary=photo")
		return r
	}
	first := request(context.Background())
	first.Header.Set("First", "true")
	go func() { h.ServeHTTP(httptest.NewRecorder(), first); close(done) }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	second := request(ctx)
	cancel()
	h.ServeHTTP(httptest.NewRecorder(), second)
	select {
	case <-entered:
		t.Fatal("cancelled waiter entered active pipeline")
	default:
	}
	close(release)
	<-done
	res := httptest.NewRecorder()
	h.ServeHTTP(res, request(context.Background()))
	if res.Code != 204 {
		t.Fatalf("slot not released: %d", res.Code)
	}
}
