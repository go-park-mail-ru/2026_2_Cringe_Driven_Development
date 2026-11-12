// Package avatar проверяет и перекодирует загруженные изображения профиля.
package avatar

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
)

var (
	ErrUnsupportedMediaType = errors.New("unsupported avatar media type")
	ErrInvalidImage         = errors.New("invalid avatar image")
)

const maxInputSize int64 = 5 * 1024 * 1024

// File владеет seekable-очищенным изображением и удаляет его при закрытии.
type File struct {
	*os.File
	ContentType string
	Size        int64
	closed      bool
}

// Close освобождает файловый дескриптор и удаляет временное изображение.
func (f *File) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	return errors.Join(f.File.Close(), os.Remove(f.Name()))
}

// Codec использует кодеки стандартной библиотеки, не удерживая закодированную загрузку.
type Codec struct{}

// Sanitize проверяет изображение и удаляет метаданные перекодированием.
// Вызывающий владеет возвращённым временным файлом и обязан его закрыть.
func (Codec) Sanitize(ctx context.Context, source io.ReadSeeker, contentType string) (_ *File, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if contentType != "image/jpeg" && contentType != "image/png" {
		return nil, ErrUnsupportedMediaType
	}
	size, err := source.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("measure avatar: %w", err)
	}
	if size <= 0 || size > maxInputSize {
		return nil, ErrInvalidImage
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind avatar: %w", err)
	}
	var header [512]byte
	n, err := io.ReadFull(source, header[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("read avatar signature: %w", err)
	}
	detected := http.DetectContentType(header[:n])
	if detected != contentType {
		if detected == "image/jpeg" || detected == "image/png" || detected == "image/gif" || detected == "image/webp" || detected == "image/bmp" || detected == "image/x-icon" {
			return nil, ErrUnsupportedMediaType
		}
		return nil, ErrInvalidImage
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind avatar: %w", err)
	}
	_, _, err = image.DecodeConfig(contextReader{ctx, source})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, ErrInvalidImage
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind avatar: %w", err)
	}
	decoded, _, err := image.Decode(contextReader{ctx, source})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, ErrInvalidImage
	}
	out, err := os.CreateTemp("", "avatar-*")
	if err != nil {
		return nil, fmt.Errorf("create sanitized avatar: %w", err)
	}
	f := &File{File: out, ContentType: contentType}
	defer func() {
		if err != nil {
			_ = f.Close()
		}
	}()
	writer := contextWriter{ctx, out}
	if contentType == "image/png" {
		flattened := image.NewRGBA(decoded.Bounds())
		draw.Draw(flattened, flattened.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
		draw.Draw(flattened, flattened.Bounds(), decoded, decoded.Bounds().Min, draw.Over)
		err = png.Encode(writer, flattened)
	} else {
		err = jpeg.Encode(writer, decoded, &jpeg.Options{Quality: 85})
	}
	if err != nil {
		return nil, fmt.Errorf("encode avatar: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	f.Size, err = out.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("measure sanitized avatar: %w", err)
	}
	if _, err = out.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind sanitized avatar: %w", err)
	}
	return f, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type contextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(p)
}
