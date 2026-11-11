package observability

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// LogStorage записывает безопасную сводку одной завершённой операции с хранилищем.
func LogStorage(ctx context.Context, operation string, started time.Time, err error) {
	outcome := "success"
	attrs := []any{slog.String("operation", operation), slog.Duration("duration", time.Since(started))}
	if err != nil {
		outcome = "error"
		code := ErrorCode(err)
		if errors.Is(err, pgx.ErrNoRows) {
			outcome = "not_found"
			code = "not_found"
		}
		attrs = append(attrs, slog.String("error_code", code))
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			attrs = append(attrs, slog.String("sqlstate", pgErr.Code))
		}
	}
	attrs = append(attrs, slog.String("outcome", outcome))
	slog.DebugContext(ctx, "storage.operation", attrs...)
}
