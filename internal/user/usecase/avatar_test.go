package usecase

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
)

type avatarRepository struct {
	UserStore
	old     string
	failure error
	saved   string
}

func (r *avatarRepository) UserByID(context.Context, int64) (domain.User, error) {
	return domain.User{ID: 1, Login: "alice"}, nil
}
func (r *avatarRepository) SwapAvatar(_ context.Context, id int64, key string) (domain.User, string, error) {
	r.saved = key
	return domain.User{ID: id, Login: "alice", AvatarKey: key}, r.old, r.failure
}

type avatarStorage struct {
	putErr, deleteErr                    error
	putKey, deleteKey, body, contentType string
	size                                 int64
}

func (s *avatarStorage) Put(_ context.Context, key string, r io.ReadSeeker, size int64, mime string) error {
	s.putKey = key
	s.size = size
	s.contentType = mime
	var b strings.Builder
	_, _ = io.Copy(&b, r)
	s.body = b.String()
	return s.putErr
}
func (s *avatarStorage) Delete(_ context.Context, key string) error {
	s.deleteKey = key
	return s.deleteErr
}

type sanitizedReader struct {
	*strings.Reader
	closed bool
}

func (r *sanitizedReader) Close() error { r.closed = true; return nil }
func TestAvatarUploadPersistence(t *testing.T) {
	failure := errors.New("storage failure")
	for _, tc := range []struct {
		name                       string
		putErr, swapErr, deleteErr error
		wantErr                    bool
	}{
		{name: "replace"}, {name: "put failure", putErr: failure, wantErr: true}, {name: "unknown database outcome", swapErr: failure, wantErr: true}, {name: "cleanup failure", deleteErr: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &avatarRepository{old: "old.png", failure: tc.swapErr}
			storage := &avatarStorage{putErr: tc.putErr, deleteErr: tc.deleteErr}
			body := &sanitizedReader{Reader: strings.NewReader("cleaned")}
			uc := NewAvatarUsecase(repo, storage, func(context.Context, io.ReadSeeker, string) (SanitizedAvatar, error) {
				return SanitizedAvatar{Body: body, Size: 7, ContentType: "image/png"}, nil
			})
			user, err := uc.Upload(context.Background(), 1, strings.NewReader("untrusted"), "image/png")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v", err)
			}
			if !body.closed {
				t.Fatal("cleaned file not closed")
			}
			if storage.body != "cleaned" || storage.size != 7 || storage.contentType != "image/png" {
				t.Fatalf("wrong upload %+v", storage)
			}
			if tc.putErr != nil && repo.saved != "" {
				t.Fatal("database changed after failed PUT")
			}
			if tc.swapErr != nil && storage.deleteKey != "" {
				t.Fatal("deleted potentially referenced object after unknown DB outcome")
			}
			if !tc.wantErr && (user.AvatarKey != storage.putKey || storage.deleteKey != "old.png" || !strings.HasSuffix(user.AvatarKey, ".png")) {
				t.Fatal("replacement ordering or key incorrect")
			}
		})
	}
}
