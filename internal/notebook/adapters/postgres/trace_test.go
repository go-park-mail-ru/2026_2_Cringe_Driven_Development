package postgres

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/jackc/pgx/v5"
)

// sqlTrace записывает текст SQL, но никогда не значения аргументов, для приёмочного
// прогона автосохранения. Включается только для измеряемых сохранений, без import/export.
type sqlTrace struct {
	mu      sync.Mutex
	enabled bool
	file    *os.File
}

func (t *sqlTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.enabled {
		_, _ = fmt.Fprintln(t.file, data.SQL)
	}
	return ctx
}
func (t *sqlTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (t *sqlTrace) enable(enabled bool)                                             { t.mu.Lock(); t.enabled = enabled; t.mu.Unlock() }
