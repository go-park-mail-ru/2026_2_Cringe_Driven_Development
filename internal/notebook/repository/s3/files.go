// Package s3 хранит файлы блокнотов в S3-совместимом хранилище.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/repository"
)

// NewClient берёт регион, ключи и адрес из переменных AWS_*.
func NewClient(ctx context.Context) (*awss3.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.UsePathStyle = true
	}), nil
}

// opTimeout меньше WRITE_TIMEOUT сервера, чтобы при недоступном S3 клиент получил 500.
const opTimeout = 5 * time.Second

// FileStorage реализует repository.Files на одном бакете.
type FileStorage struct {
	client *awss3.Client
	bucket string
}

var _ repository.Files = (*FileStorage)(nil)

// NewFileStorage принимает клиент из NewClient и имя бакета.
func NewFileStorage(client *awss3.Client, bucket string) *FileStorage {
	return &FileStorage{client: client, bucket: bucket}
}

// Get возвращает repository.ErrFileNotFound, если ключа нет.
func (s *FileStorage) Get(ctx context.Context, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	out, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	var noKey *types.NoSuchKey
	if errors.As(err, &noKey) {
		return nil, repository.ErrFileNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get object %s: %w", key, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("read object %s: %w", key, err)
	}
	return data, nil
}

// Put записывает файл, заменяя прежний с тем же ключом.
func (s *FileStorage) Put(ctx context.Context, key string, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	_, err := s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/x-ipynb+json"),
	})
	if err != nil {
		return fmt.Errorf("put object %s: %w", key, err)
	}
	return nil
}
