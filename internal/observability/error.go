package observability

import "errors"

// codedError хранит безопасную категорию и исходную ошибку.
type codedError struct {
	err  error
	code string
}

// Error возвращает сообщение исходной ошибки.
func (e *codedError) Error() string { return e.err.Error() }

// Unwrap сохраняет цепочку ошибок для errors.Is и errors.As.
func (e *codedError) Unwrap() error { return e.err }

// WithCode связывает ошибку err со стабильной категорией code для логов.
func WithCode(err error, code string) error {
	if err == nil {
		return nil
	}
	return &codedError{err: err, code: code}
}

// ErrorCode возвращает категорию ошибки err для логов.
func ErrorCode(err error) string {
	var coded *codedError
	if errors.As(err, &coded) {
		return coded.code
	}
	return "internal_error"
}
