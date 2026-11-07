// Package s3client creates the shared AWS client used by avatars.
package s3client

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
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
