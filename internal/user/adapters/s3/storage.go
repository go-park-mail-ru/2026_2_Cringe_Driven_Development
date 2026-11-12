// Package s3 потоково отправляет очищенные файлы аватаров в S3-совместимое хранилище.
package s3

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/observability"
)

const operationTimeout = 5 * time.Second

// Storage хранит объекты аватаров в одном бакете.
type Storage struct {
	client *awss3.Client
	bucket string
}

// NewStorage привязывает существующий AWS-клиент к бакету аватаров.
func NewStorage(client *awss3.Client, bucket string) *Storage {
	return &Storage{client: client, bucket: bucket}
}

// Put потоково отправляет seekable-изображение известного размера без буферизации в памяти.
func (s *Storage) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	_, err := s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: body,
		ContentLength: aws.Int64(size), ContentType: aws.String(contentType),
	}, func(o *awss3.Options) {
		o.RetryMaxAttempts = 1
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})
	observability.LogStorage(ctx, "avatar.put_object", started, err)
	if err != nil {
		return fmt.Errorf("put avatar object: %w", err)
	}
	return nil
}

// Delete удаляет один объект; мутации никогда не повторяются автоматически.
func (s *Storage) Delete(ctx context.Context, key string) error {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	_, err := s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}, func(o *awss3.Options) { o.RetryMaxAttempts = 1 })
	observability.LogStorage(ctx, "avatar.delete_object", started, err)
	if err != nil {
		return fmt.Errorf("delete avatar object: %w", err)
	}
	return nil
}
