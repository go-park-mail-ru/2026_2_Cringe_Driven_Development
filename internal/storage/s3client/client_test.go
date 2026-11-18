package s3client

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/observability"
)

type httpClientFunc func(*http.Request) (*http.Response, error)

func (f httpClientFunc) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestNewClientLogsS3OperationsWithoutObjectData(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "SECRET-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SECRET-key")
	var output bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(observability.NewHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	t.Cleanup(func() { slog.SetDefault(old) })

	ctx := observability.WithRequestID(context.Background(), "req-s3")
	client, err := NewClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("SECRET-object"),
	}, func(options *awss3.Options) {
		options.HTTPClient = httpClientFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader(""))}, nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	log := output.String()
	if strings.Count(log, `"operation":"s3.DeleteObject"`) != 1 ||
		!strings.Contains(log, `"request_id":"req-s3"`) || strings.Contains(log, "SECRET") {
		t.Fatalf("missing correlation or unsafe data: %s", log)
	}
}
