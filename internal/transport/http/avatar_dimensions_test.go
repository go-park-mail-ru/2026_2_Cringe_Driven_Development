package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"

	avatarcodec "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/avatar"
	usersecurity "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/adapters/security"
	userdelivery "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/delivery/http"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
)

type avatarDimensionRepository struct{ user domain.User }

func (r *avatarDimensionRepository) UserByID(_ context.Context, id int64) (domain.User, error) {
	if id != r.user.ID {
		return domain.User{}, usecase.ErrUserNotFound
	}
	return r.user, nil
}

func (r *avatarDimensionRepository) SwapAvatar(ctx context.Context, id int64, key string) (domain.User, string, error) {
	if _, err := r.UserByID(ctx, id); err != nil {
		return domain.User{}, "", err
	}
	old := r.user.AvatarKey
	r.user.AvatarKey = key
	return r.user, old, nil
}

type avatarDimensionStorage struct {
	key, mime, deleted string
	data               []byte
	size               int64
}

func (s *avatarDimensionStorage) Put(_ context.Context, key string, source io.ReadSeeker, size int64, mime string) error {
	data, err := io.ReadAll(source)
	if err != nil {
		return err
	}
	s.key, s.mime, s.data, s.size = key, mime, data, size
	return nil
}

func (s *avatarDimensionStorage) Delete(_ context.Context, key string) error {
	s.deleted = key
	return nil
}

func avatarDimensionImage(t *testing.T, size image.Point, format string) []byte {
	t.Helper()
	var b bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, size.X, size.Y))
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

type avatarDimensionFixture struct {
	handler         http.Handler
	repo            *avatarDimensionRepository
	storage         *avatarDimensionStorage
	access, tempDir string
}

func newAvatarDimensionFixture(t *testing.T) avatarDimensionFixture {
	t.Helper()
	f := avatarDimensionFixture{
		repo:    &avatarDimensionRepository{user: domain.User{ID: 42, Login: "alice", AvatarKey: "avatars/old.png"}},
		storage: &avatarDimensionStorage{}, tempDir: t.TempDir(),
	}
	t.Setenv("TMPDIR", f.tempDir)
	avatars := usecase.NewAvatarUsecase(f.repo, f.storage, func(ctx context.Context, source io.ReadSeeker, mime string) (usecase.SanitizedAvatar, error) {
		file, err := (avatarcodec.Codec{}).Sanitize(ctx, source, mime)
		switch {
		case errors.Is(err, avatarcodec.ErrInvalidImage):
			return usecase.SanitizedAvatar{}, usecase.ErrInvalidImage
		case errors.Is(err, avatarcodec.ErrUnsupportedMediaType):
			return usecase.SanitizedAvatar{}, usecase.ErrUnsupportedMediaType
		case err != nil:
			return usecase.SanitizedAvatar{}, err
		default:
			return usecase.SanitizedAvatar{Body: file, Size: file.Size, ContentType: file.ContentType}, nil
		}
	})
	tokens := usersecurity.NewAccessToken([]byte("avatar-dimension-test"), time.Hour)
	var err error
	f.access, err = tokens.Issue(testClaims(42))
	if err != nil {
		t.Fatal(err)
	}
	f.handler, err = NewRouter(Server{UserHandler: userdelivery.NewHandler(&profileUsers{}, userdelivery.WithProfile(avatars, "https://avatars.example.com"))}, tokens, allowSessions{}, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f avatarDimensionFixture) upload(t *testing.T, data []byte, mime string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="avatar"; filename="image"`)
	header.Set("Content-Type", mime)
	part, err := w.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPut, BaseURL+"/users/me/avatar", &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	r.Header.Set("Authorization", "Bearer "+f.access)
	r.Header.Set("X-BFF-Key", "test-key")
	rec := serveRouter(f.handler, r)
	files, err := os.ReadDir(f.tempDir)
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary uploads retained: %v %v", files, err)
	}
	return rec
}

func assertAvatarDimensionUpload(t *testing.T, f avatarDimensionFixture, rec *httptest.ResponseRecorder, size image.Point, format string) {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var user struct {
		ID        int64  `json:"id"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &user); err != nil {
		t.Fatal(err)
	}
	if user.ID != 42 || user.AvatarURL != "https://avatars.example.com/"+f.repo.user.AvatarKey || f.repo.user.AvatarKey != f.storage.key || f.storage.key == "avatars/old.png" || !strings.HasPrefix(f.storage.key, "avatars/") || f.storage.deleted != "avatars/old.png" {
		t.Fatalf("profile does not reference published avatar: %+v %+v", user, f.storage)
	}
	if f.storage.mime != "image/"+format || f.storage.size != int64(len(f.storage.data)) || f.storage.size <= 0 {
		t.Fatal("invalid published image metadata")
	}
	img, gotFormat, err := image.Decode(bytes.NewReader(f.storage.data))
	if err != nil || gotFormat != format {
		t.Fatalf("published image: %s %v", gotFormat, err)
	}
	if img.Bounds().Dx() != size.X || img.Bounds().Dy() != size.Y {
		t.Fatalf("resized avatar: %v want %v", img.Bounds(), size)
	}
}

func TestAvatarDimensionsUpload(t *testing.T) {
	for _, format := range []string{"png", "jpeg"} {
		for _, size := range []image.Point{{1, 1}, {199, 300}, {300, 199}, {4097, 200}, {200, 4097}} {
			t.Run(format+size.String(), func(t *testing.T) {
				f := newAvatarDimensionFixture(t)
				rec := f.upload(t, avatarDimensionImage(t, size, format), "image/"+format)
				assertAvatarDimensionUpload(t, f, rec, size, format)
			})
		}
	}
}

func TestAvatarDimensionsRejectInvalidUploads(t *testing.T) {
	pngData := avatarDimensionImage(t, image.Pt(200, 200), "png")
	smallPNG := avatarDimensionImage(t, image.Pt(1, 1), "png")
	for _, tc := range []struct {
		name, mime string
		data       []byte
		status     int
		code       string
	}{
		{"empty", "image/png", nil, 400, "validation_error"},
		{"corrupt", "image/png", []byte("invalid"), 400, "validation_error"},
		{"truncated", "image/png", pngData[:len(pngData)-20], 400, "validation_error"},
		{"unsupported", "image/gif", []byte("GIF89a"), 415, "unsupported_media_type"},
		{"mismatch", "image/jpeg", pngData, 415, "unsupported_media_type"},
		{"small mismatch", "image/jpeg", smallPNG, 415, "unsupported_media_type"},
		{"too large", "image/png", make([]byte, 5242881), 413, "payload_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAvatarDimensionFixture(t)
			rec := f.upload(t, tc.data, tc.mime)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if f.repo.user.AvatarKey != "avatars/old.png" || f.storage.key != "" || f.storage.deleted != "" {
				t.Fatal("invalid upload changed profile or object storage")
			}
		})
	}
}

func TestAvatarDimensionsInputSizeBoundary(t *testing.T) {
	for _, size := range []int{5242880, 5242881} {
		t.Run(map[int]string{5242880: "at limit", 5242881: "above limit"}[size], func(t *testing.T) {
			data := make([]byte, size)
			copy(data, avatarDimensionImage(t, image.Pt(1, 1), "png"))
			f := newAvatarDimensionFixture(t)
			rec := f.upload(t, data, "image/png")
			if size == 5242880 {
				assertAvatarDimensionUpload(t, f, rec, image.Pt(1, 1), "png")
			} else if rec.Code != 413 || f.storage.key != "" || f.repo.user.AvatarKey != "avatars/old.png" {
				t.Fatalf("oversized upload: %d %s", rec.Code, rec.Body)
			}
		})
	}
}
