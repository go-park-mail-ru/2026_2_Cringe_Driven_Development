package observability

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

// PostgresTracer пишет результат запросов и пакетов без SQL и аргументов.
type PostgresTracer struct{}

// queryStartKey изолирует время начала PostgreSQL-операции в контексте.
type queryStartKey struct{}

// TraceQueryStart сохраняет время начала запроса в его контексте.
func (PostgresTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryStartKey{}, time.Now())
}

// TraceQueryEnd журналирует результат завершённого PostgreSQL-запроса.
func (PostgresTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	started, _ := ctx.Value(queryStartKey{}).(time.Time)
	LogStorage(ctx, "postgres.query", started, data.Err)
}

// TraceBatchStart сохраняет время начала всего пакета запросов.
func (tracer PostgresTracer) TraceBatchStart(ctx context.Context, conn *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return tracer.TraceQueryStart(ctx, conn, pgx.TraceQueryStartData{})
}

// TraceBatchQuery оставляет журналирование результата обработчику всего пакета.
func (PostgresTracer) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {
}

// TraceBatchEnd журналирует результат и длительность пакета запросов.
func (PostgresTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	started, _ := ctx.Value(queryStartKey{}).(time.Time)
	LogStorage(ctx, "postgres.batch", started, data.Err)
}

// RedisHook журналирует команды и пакеты команд без ключей и значений.
type RedisHook struct{}

// DialHook сохраняет штатную установку соединения Redis.
func (RedisHook) DialHook(next redis.DialHook) redis.DialHook { return next }

// ProcessHook измеряет одиночную команду от входа в клиент до результата.
func (RedisHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		started := time.Now()
		err := next(ctx, command)
		LogStorage(ctx, "redis."+command.Name(), started, err)
		return err
	}
}

// ProcessPipelineHook пишет одну сводку для всего пакета команд.
func (RedisHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, commands []redis.Cmder) error {
		started := time.Now()
		err := next(ctx, commands)
		LogStorage(ctx, "redis.pipeline", started, err)
		return err
	}
}

var (
	_ pgx.QueryTracer = PostgresTracer{}
	_ pgx.BatchTracer = PostgresTracer{}
	_ redis.Hook      = RedisHook{}
)
