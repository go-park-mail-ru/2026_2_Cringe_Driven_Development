package s3

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

type largeObjectTransport struct{ read int }
type largeObjectBody struct {
	transport *largeObjectTransport
	remaining int
}

func (b *largeObjectBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(p), b.remaining)
	clear(p[:n])
	b.remaining -= n
	b.transport.read += n
	return n, nil
}
func (*largeObjectBody) Close() error { return nil }
func (tr *largeObjectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &largeObjectBody{tr, 256 * 1024 * 1024}, ContentLength: -1, Request: r}, nil
}
func TestGetLimitsUnknownLengthObject(t *testing.T) {
	tr := &largeObjectTransport{}
	client := awss3.New(awss3.Options{Region: "test", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
	}), HTTPClient: &http.Client{Transport: tr}, BaseEndpoint: aws.String("http://s3.test")}, func(o *awss3.Options) { o.UsePathStyle = true })
	_, err := NewFileStorage(client, "test").Get(context.Background(), "large")
	if err == nil {
		t.Fatal("oversized S3 object accepted")
	}
	if tr.read > 128*1024*1024+1 {
		t.Fatalf("read %d bytes from an oversized object", tr.read)
	}
}
func TestPutRejectsOversizedObjectBeforeNetwork(t *testing.T) {
	storage := NewFileStorage(nil, "test")
	if err := storage.Put(context.Background(), "large", []byte(strings.Repeat("x", 128*1024*1024+1))); err == nil {
		t.Fatal("oversized upload accepted")
	}
}
