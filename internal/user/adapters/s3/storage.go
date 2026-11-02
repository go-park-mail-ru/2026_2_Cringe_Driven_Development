// Package s3 streams sanitized avatar files to S3-compatible storage.
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

// Storage keeps avatar objects in one bucket.
type Storage struct {
	client *awss3.Client
	bucket string
}

// NewStorage binds the existing AWS client to the avatars bucket.
func NewStorage(client *awss3.Client, bucket string) *Storage {
	return &Storage{client: client, bucket: bucket}
}

// Put streams a seekable, known-size image without buffering it in memory.
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

// Delete removes one object; mutations are never automatically retried.
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
