// Package s3client создаёт общий AWS-клиент, используемый аватарами.
package s3client

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/middleware"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/observability"
)

// NewClient берёт регион, ключи и адрес из переменных AWS_*.
func NewClient(ctx context.Context) (*awss3.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.UsePathStyle = true
		o.APIOptions = append(o.APIOptions, logOperation)
	}), nil
}

// logOperation измеряет вызов SDK целиком, включая повторы.
func logOperation(stack *middleware.Stack) error {
	return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("StorageLog",
		func(ctx context.Context, input middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
			started := time.Now()
			output, metadata, err := next.HandleInitialize(ctx, input)
			observability.LogStorage(ctx, "s3."+middleware.GetOperationName(ctx), started, err)
			return output, metadata, err
		}), middleware.Before)
}
