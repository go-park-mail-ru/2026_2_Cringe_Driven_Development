package observability

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/redis/go-redis/v9"
)

func TestDriverLogsCorrelateWithoutSendingInputs(t *testing.T) {
	var output bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(NewHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	t.Cleanup(func() { slog.SetDefault(old) })

	ctx := WithRequestID(context.Background(), "req-drivers")
	tracer := PostgresTracer{}
	query := tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SECRET-sql", Args: []any{"SECRET-param"}})
	tracer.TraceQueryEnd(query, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "23505", Detail: "SECRET-detail"}})
	batch := tracer.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{})
	tracer.TraceBatchEnd(batch, nil, pgx.TraceBatchEndData{})

	failure := errors.New("SECRET-redis-error")
	command := redis.NewStringCmd(ctx, "get", "SECRET-key")
	hook := RedisHook{}
	if err := hook.ProcessHook(func(context.Context, redis.Cmder) error { return failure })(ctx, command); !errors.Is(err, failure) {
		t.Fatalf("process returned %v", err)
	}
	if err := hook.ProcessPipelineHook(func(context.Context, []redis.Cmder) error { return nil })(ctx, []redis.Cmder{command}); err != nil {
		t.Fatal(err)
	}

	for _, operation := range []string{"postgres.query", "postgres.batch", "redis.get", "redis.pipeline"} {
		if strings.Count(output.String(), `"operation":"`+operation+`"`) != 1 {
			t.Fatalf("expected one log for %s: %s", operation, output.String())
		}
	}
	if text := output.String(); strings.Count(text, `"request_id":"req-drivers"`) != 4 ||
		!strings.Contains(text, `"sqlstate":"23505"`) || strings.Contains(text, "SECRET") {
		t.Fatalf("missing correlation or unsafe data: %s", text)
	}
}
