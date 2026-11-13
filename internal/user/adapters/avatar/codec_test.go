package avatar

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func encoded(t *testing.T, width, height int, format string) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 128})
	var b bytes.Buffer
	var err error
	if format == "png" {
		err = png.Encode(&b, img)
	} else {
		err = jpeg.Encode(&b, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestSanitizePNGFlattensAlphaAndRemovesMetadata(t *testing.T) {
	for _, size := range []image.Point{{64, 64}, {200, 200}} {
		t.Run(size.String(), func(t *testing.T) {
			input := encoded(t, size.X, size.Y, "png")
			// Валидный вспомогательный текстовый чанк не должен пережить перекодирование.
			chunk := []byte("tEXtprivate\x00secret")
			var metadata bytes.Buffer
			_ = binary.Write(&metadata, binary.BigEndian, uint32(len(chunk)-4))
			metadata.Write(chunk)
			_ = binary.Write(&metadata, binary.BigEndian, crc32.ChecksumIEEE(chunk))
			input = append(append(append([]byte{}, input[:33]...), metadata.Bytes()...), input[33:]...)
			f, err := (Codec{}).Sanitize(context.Background(), bytes.NewReader(input), "image/png")
			if err != nil {
				t.Fatal(err)
			}
			name := f.Name()
			defer f.Close()
			st, err := f.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if st.Mode().Perm() != 0600 || f.Size != st.Size() || f.ContentType != "image/png" {
				t.Fatalf("file metadata: mode=%v size=%d stat=%d type=%s", st.Mode(), f.Size, st.Size(), f.ContentType)
			}
			output, err := io.ReadAll(f)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(output, []byte("secret")) {
				t.Fatal("metadata survived")
			}
			img, err := png.Decode(bytes.NewReader(output))
			if err != nil {
				t.Fatal(err)
			}
			if size.X > 1 {
				if got := color.NRGBAModel.Convert(img.At(1, 1)).(color.NRGBA); got != (color.NRGBA{255, 255, 255, 255}) {
					t.Fatalf("transparent pixel = %v", got)
				}
			}
			if got := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); got != (color.NRGBA{255, 127, 127, 255}) {
				t.Fatalf("translucent pixel = %v", got)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("temporary file retained: %v", err)
			}
		})
	}
}

func TestSanitizeRejectsInvalidInputs(t *testing.T) {
	pngData := encoded(t, 200, 200, "png")
	for _, tt := range []struct {
		name, mime string
		data       []byte
		want       error
	}{
		{"empty", "image/png", nil, ErrInvalidImage},
		{"corrupt", "image/png", []byte("bad"), ErrInvalidImage},
		{"unsupported", "image/gif", []byte("GIF89a"), ErrUnsupportedMediaType},
		{"mismatch", "image/jpeg", pngData, ErrUnsupportedMediaType},
		{"too large", "image/png", make([]byte, 5*1024*1024+1), ErrInvalidImage},
		{"truncated", "image/png", pngData[:len(pngData)-20], ErrInvalidImage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f, err := (Codec{}).Sanitize(context.Background(), bytes.NewReader(tt.data), tt.mime)
			if f != nil {
				f.Close()
				t.Fatal("unexpected file")
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("error=%v want=%v", err, tt.want)
			}
		})
	}
}

func TestSanitizeDimensionBoundaries(t *testing.T) {
	for _, format := range []string{"png", "jpeg"} {
		for _, size := range []image.Point{{64, 64}, {64, 199}, {199, 64}, {199, 300}, {300, 199}, {64, 4096}, {4096, 64}, {200, 200}, {4096, 200}, {200, 4096}} {
			t.Run(format+size.String(), func(t *testing.T) {
				f, err := (Codec{}).Sanitize(t.Context(), bytes.NewReader(encoded(t, size.X, size.Y, format)), "image/"+format)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				name := f.Name()
				stat, err := f.Stat()
				if err != nil {
					t.Fatal(err)
				}
				if f.ContentType != "image/"+format || f.Size <= 0 || f.Size != stat.Size() {
					t.Fatal("invalid sanitized file metadata")
				}
				cfg, gotFormat, err := image.DecodeConfig(f)
				if err != nil || cfg.Width != size.X || cfg.Height != size.Y || gotFormat != format {
					t.Fatalf("resized or wrong format: %+v %s %v", cfg, gotFormat, err)
				}
				if _, err = f.Seek(0, io.SeekStart); err != nil {
					t.Fatal(err)
				}
				if _, _, err = image.Decode(f); err != nil {
					t.Fatal(err)
				}
				if err = f.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err = os.Stat(name); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("temporary file retained: %v", err)
				}
			})
		}
	}
}

func TestSanitizeRejectsDimensions(t *testing.T) {
	for _, format := range []string{"png", "jpeg"} {
		for _, size := range []image.Point{{1, 1}, {63, 200}, {200, 63}, {4097, 200}, {200, 4097}, {63, 64}, {64, 63}} {
			t.Run(format+size.String(), func(t *testing.T) {
				f, err := (Codec{}).Sanitize(t.Context(), bytes.NewReader(encoded(t, size.X, size.Y, format)), "image/"+format)
				if f != nil {
					_ = f.Close()
					t.Fatal("out-of-range image produced file")
				}
				if !errors.Is(err, ErrInvalidImage) {
					t.Fatalf("dimensions %v: %v", size, err)
				}
			})
		}
	}
}

// decodeGuardReader rejects the rewind that would start full image decoding.
// DecodeConfig and MIME detection may read headers, without allocating pixel buffers.
type decodeGuardReader struct {
	*bytes.Reader
	rewinds    int
	fullDecode bool
}

func (r *decodeGuardReader) Seek(offset int64, whence int) (int64, error) {
	if offset == 0 && whence == io.SeekStart {
		r.rewinds++
		if r.rewinds == 3 {
			r.fullDecode = true
			return 0, errors.New("unexpected full decode of invalid dimensions")
		}
	}
	return r.Reader.Seek(offset, whence)
}

func TestSanitizeChecksDimensionsBeforeDecode(t *testing.T) {
	for _, format := range []string{"png", "jpeg"} {
		for _, size := range []image.Point{{63, 200}, {200, 63}, {4097, 200}, {200, 4097}} {
			t.Run(format+size.String(), func(t *testing.T) {
				source := &decodeGuardReader{Reader: bytes.NewReader(encoded(t, size.X, size.Y, format))}
				f, err := (Codec{}).Sanitize(t.Context(), source, "image/"+format)
				if f != nil {
					_ = f.Close()
					t.Fatal("invalid dimensions produced file")
				}
				if !errors.Is(err, ErrInvalidImage) || source.fullDecode {
					t.Fatalf("rejection must precede full decode: fullDecode=%v err=%v", source.fullDecode, err)
				}
			})
		}
	}
}

func TestSanitizeRejectsInvalidImageData(t *testing.T) {
	pngData := encoded(t, 200, 200, "png")
	jpegData := encoded(t, 200, 200, "jpeg")
	badHeader := bytes.Clone(pngData)
	badHeader[29] ^= 0xff // Invalid IHDR CRC, without huge allocation.
	for _, tc := range []struct {
		name, mime  string
		data        []byte
		validHeader bool
	}{
		{"bad PNG header", "image/png", badHeader, false},
		{"truncated PNG", "image/png", pngData[:len(pngData)-20], true},
		{"truncated JPEG", "image/jpeg", jpegData[:len(jpegData)-20], true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.validHeader {
				if _, _, err := image.DecodeConfig(bytes.NewReader(tc.data)); err != nil {
					t.Fatal("fixture must have a valid header", err)
				}
			}
			f, err := (Codec{}).Sanitize(t.Context(), bytes.NewReader(tc.data), tc.mime)
			if f != nil {
				_ = f.Close()
				t.Fatal("invalid image produced file")
			}
			if !errors.Is(err, ErrInvalidImage) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestSanitizeJPEGRemovesComment(t *testing.T) {
	for _, size := range []image.Point{{64, 64}, {200, 200}} {
		t.Run(size.String(), func(t *testing.T) {
			data := encoded(t, size.X, size.Y, "jpeg")
			marker := []byte{0xff, 0xfe, 0, 8, 's', 'e', 'c', 'r', 'e', 't'}
			data = append(append(append([]byte{}, data[:2]...), marker...), data[2:]...)
			f, err := (Codec{}).Sanitize(context.Background(), bytes.NewReader(data), "image/jpeg")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			output, err := io.ReadAll(f)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(output, []byte("secret")) {
				t.Fatal("JPEG comment survived")
			}
			cfg, err := jpeg.DecodeConfig(bytes.NewReader(output))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Width != size.X || cfg.Height != size.Y || f.ContentType != "image/jpeg" {
				t.Fatalf("JPEG metadata: %v %s", cfg, f.ContentType)
			}
		})
	}
}

func TestSanitizeCanceledLeavesNoFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (Codec{}).Sanitize(ctx, bytes.NewReader(encoded(t, 200, 200, "png")), "image/png")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("files left=%v", files)
	}
}

// cancelWhenOutputExists детерминированно отменяет во время записи закодированного
// результата, поэтому тест покрывает очистку после создания файла.
type cancelWhenOutputExists struct {
	context.Context
	dir    string
	cancel context.CancelFunc
}

func (c cancelWhenOutputExists) Err() error {
	entries, err := os.ReadDir(c.dir)
	if err == nil && len(entries) > 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestSanitizeCancellationDuringEncodeRemovesOutput(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := cancelWhenOutputExists{Context: base, dir: dir, cancel: cancel}
	f, err := (Codec{}).Sanitize(ctx, bytes.NewReader(encoded(t, 200, 200, "png")), "image/png")
	if f != nil {
		f.Close()
		t.Fatal("returned file on cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary outputs remain: %v", entries)
	}
}
