package s3

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

func testStorage(t *testing.T, handler http.HandlerFunc) *Storage {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := awss3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}, func(o *awss3.Options) { o.BaseEndpoint = aws.String(server.URL); o.UsePathStyle = true })
	return NewStorage(client, "avatars")
}

func TestPutStreamsSeekableFileWithMetadata(t *testing.T) {
	payload := strings.Repeat("avatar", 20000)
	var called atomic.Bool
	store := testStorage(t, func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		if r.Method != "PUT" || r.URL.Path != "/avatars/users/test.png" {
			t.Errorf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "image/png" {
			t.Errorf("content type=%s", r.Header.Get("Content-Type"))
		}
		if r.ContentLength != int64(len(payload)) {
			t.Errorf("content length=%d want=%d", r.ContentLength, len(payload))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if string(body) != payload {
			t.Errorf("body length=%d want=%d", len(body), len(payload))
		}
		w.WriteHeader(http.StatusOK)
	})
	file, err := os.CreateTemp(t.TempDir(), "avatar")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err = file.WriteString(payload); err != nil {
		t.Fatal(err)
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if err = store.Put(context.Background(), "users/test.png", file, int64(len(payload)), "image/png"); err != nil {
		t.Fatal(err)
	}
	if !called.Load() {
		t.Fatal("no S3 request")
	}
}

func TestMutationFailuresAreNotRetried(t *testing.T) {
	for _, operation := range []string{"put", "delete"} {
		t.Run(operation, func(t *testing.T) {
			var requests atomic.Int32
			store := testStorage(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("<Error><Code>SlowDown</Code><Message>unavailable</Message></Error>"))
			})
			var err error
			if operation == "put" {
				err = store.Put(context.Background(), "private-key", bytes.NewReader([]byte("png")), 3, "image/png")
			} else {
				err = store.Delete(context.Background(), "private-key")
			}
			if err == nil {
				t.Fatal("expected S3 failure")
			}
			if requests.Load() != 1 {
				t.Fatalf("requests=%d", requests.Load())
			}
		})
	}
}

func TestDeleteAndCancellation(t *testing.T) {
	var requests atomic.Int32
	store := testStorage(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "DELETE" || r.URL.Path != "/avatars/old.jpeg" {
			t.Errorf("request=%s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := store.Delete(context.Background(), "old.jpeg"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Delete(ctx, "old.jpeg"); err == nil {
		t.Fatal("expected cancellation")
	}
	if requests.Load() != 1 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestMutationHonorsCallerDeadline(t *testing.T) {
	store := testStorage(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := store.Delete(ctx, "old.jpeg"); err == nil {
		t.Fatal("expected deadline")
	}
}
