package s3

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/repository"
)

// TestFileStorage ходит в настоящий S3 и без S3_TEST_BUCKET пропускается.
func TestFileStorage(t *testing.T) {
	bucket := os.Getenv("S3_TEST_BUCKET")
	if bucket == "" {
		t.Skip("S3_TEST_BUCKET не задан")
	}
	ctx := context.Background()
	client, err := NewClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	files := NewFileStorage(client, bucket)
	key := "test/" + t.Name() + ".ipynb"

	want := []byte(`{"cells": []}`)
	if err = files.Put(ctx, key, want); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	got, err := files.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("Get() = %s, want %s", got, want)
	}
	if _, err = files.Get(ctx, "test/missing.ipynb"); !errors.Is(err, repository.ErrFileNotFound) {
		t.Errorf("Get() отсутствующего: error = %v, want ErrFileNotFound", err)
	}
}
