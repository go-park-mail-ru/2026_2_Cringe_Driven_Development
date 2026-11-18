package middleware

import (
	"bufio"
	"io"
	"net"
	"net/http"
)

// responseWriter запоминает первый код ответа и передаёт запись в исходный writer.
type responseWriter struct {
	http.ResponseWriter
	status int
}

// newResponseWriter оборачивает HTTP writer для учёта статуса.
func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w}
}

// Unwrap возвращает исходный HTTP writer для контроллера ответа.
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// WriteHeader запоминает первый HTTP-статус и передаёт его клиенту.
func (w *responseWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Write записывает тело HTTP-ответа и отмечает неявный статус.
func (w *responseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

// flushingWriter сохраняет доступ к flush исходного HTTP writer.
type flushingWriter struct{ *responseWriter }

// withFlush возвращает обёртку с flush, когда его поддерживает транспорт.
func (w *responseWriter) withFlush() http.ResponseWriter {
	if _, ok := w.ResponseWriter.(http.Flusher); ok {
		return &flushingWriter{w}
	}
	if _, ok := w.ResponseWriter.(interface{ FlushError() error }); ok {
		return &flushingWriter{w}
	}
	return w
}

// Flush отправляет накопленный ответ, сохраняя запись HTTP-статуса.
func (w *flushingWriter) Flush() {
	_ = w.FlushError()
}

// FlushError отправляет накопленный ответ и сообщает ошибку транспорта.
func (w *flushingWriter) FlushError() error {
	if err := http.NewResponseController(w.ResponseWriter).Flush(); err != nil {
		return err
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return nil
}

// Hijack передаёт владение соединением вызывающему через исходный writer.
func (w *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// Push передаёт запрос HTTP/2 push исходному writer.
func (w *responseWriter) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}

// ReadFrom записывает поток reader в ответ с учётом HTTP-статуса.
func (w *responseWriter) ReadFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if readerFrom, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return readerFrom.ReadFrom(r)
	}
	return io.Copy(struct{ io.Writer }{w.ResponseWriter}, r)
}
