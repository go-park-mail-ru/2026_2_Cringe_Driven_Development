package observability

import (
	"context"
	"log/slog"
)

// requestHandler добавляет идентификатор к структурированным записям.
type requestHandler struct{ next slog.Handler }

// NewHandler оборачивает обработчик next и переносит request ID из контекста.
func NewHandler(next slog.Handler) slog.Handler { return requestHandler{next: next} }

// Enabled сохраняет фильтрацию уровней вложенного обработчика.
func (h requestHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle добавляет request ID к записи перед её отправкой.
func (h requestHandler) Handle(ctx context.Context, record slog.Record) error {
	if id := RequestID(ctx); id != "" {
		record = record.Clone()
		record.AddAttrs(slog.String("request_id", id))
	}
	return h.next.Handle(ctx, record)
}

// WithAttrs сохраняет корреляцию при добавлении постоянных полей.
func (h requestHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return requestHandler{next: h.next.WithAttrs(attrs)}
}

// WithGroup сохраняет корреляцию во вложенной группе полей.
func (h requestHandler) WithGroup(name string) slog.Handler {
	return requestHandler{next: h.next.WithGroup(name)}
}
