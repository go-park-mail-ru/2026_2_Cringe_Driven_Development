// Package s3 provides access to legacy S3 objects for offline notebook migration.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/observability"
)

// Offline transfer may read a full 128 MiB object over a remote S3 connection.
// Ordinary API requests use PostgreSQL and never use this adapter.
const opTimeout = 2 * time.Minute

// FileStorage reads the legacy bucket for migration; it is not wired into the API.
type FileStorage struct {
	client *awss3.Client
	bucket string
}

// NewFileStorage принимает клиент из NewClient и имя бакета.
func NewFileStorage(client *awss3.Client, bucket string) *FileStorage {
	return &FileStorage{client: client, bucket: bucket}
}

// Get возвращает usecase.ErrFileNotFound, если ключа нет.
func (s *FileStorage) Get(ctx context.Context, key string) ([]byte, error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	out, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	var noKey *types.NoSuchKey
	if errors.As(err, &noKey) {
		observability.LogStorage(ctx, "notebook.get_object", started, err)
		return nil, usecase.ErrFileNotFound
	}
	if err != nil {
		observability.LogStorage(ctx, "notebook.get_object", started, err)
		return nil, fmt.Errorf("get object %s: %w", key, err)
	}
	defer out.Body.Close()
	if out.ContentLength != nil && *out.ContentLength > usecase.MaxNotebookBytes {
		observability.LogStorage(ctx, "notebook.get_object", started, usecase.ErrTooLarge)
		return nil, usecase.ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(out.Body, usecase.MaxNotebookBytes+1))
	if err == nil && len(data) > usecase.MaxNotebookBytes {
		err = usecase.ErrTooLarge
	}
	observability.LogStorage(ctx, "notebook.get_object", started, err)
	if err != nil {
		return nil, fmt.Errorf("read object %s: %w", key, err)
	}
	return data, nil
}

// Put записывает файл, заменяя прежний с тем же ключом.
func (s *FileStorage) Put(ctx context.Context, key string, data []byte) error {
	if len(data) > usecase.MaxNotebookBytes {
		return usecase.ErrTooLarge
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	_, err := s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/x-ipynb+json"),
	})
	observability.LogStorage(ctx, "notebook.put_object", started, err)
	if err != nil {
		return fmt.Errorf("put object %s: %w", key, err)
	}
	return nil
}
