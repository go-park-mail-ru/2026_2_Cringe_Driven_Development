package usecase

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"github.com/google/uuid"
)

// Ошибки аватара описывают валидацию независимо от адаптеров изображений и HTTP.
var (
	ErrInvalidImage         = errors.New("invalid avatar image")
	ErrUnsupportedMediaType = errors.New("unsupported avatar media type")
	ErrAvatarTooLarge       = errors.New("avatar exceeds upload size limit")
)

// SanitizedAvatar владеет временным очищенным файлом, Close его удаляет.
type SanitizedAvatar struct {
	Body        io.ReadSeekCloser
	Size        int64
	ContentType string
}

type AvatarSanitizer func(context.Context, io.ReadSeeker, string) (SanitizedAvatar, error)

type AvatarStorage interface {
	Put(context.Context, string, io.ReadSeeker, int64, string) error
	Delete(context.Context, string) error
}

type AvatarRepository interface {
	UserByID(context.Context, int64) (domain.User, error)
	SwapAvatar(context.Context, int64, string) (domain.User, string, error)
}

// AvatarUsecase публикует новый объект до изменения ссылки в профиле.
type AvatarUsecase struct {
	users    AvatarRepository
	storage  AvatarStorage
	sanitize AvatarSanitizer
}

func NewAvatarUsecase(users AvatarRepository, storage AvatarStorage, sanitize AvatarSanitizer) *AvatarUsecase {
	return &AvatarUsecase{users: users, storage: storage, sanitize: sanitize}
}

func (u *AvatarUsecase) Upload(ctx context.Context, id int64, source io.ReadSeeker, mime string) (domain.User, error) {
	if _, err := u.users.UserByID(ctx, id); err != nil {
		return domain.User{}, err
	}
	cleaned, err := u.sanitize(ctx, source, mime)
	if err != nil {
		return domain.User{}, err
	}
	defer func() { _ = cleaned.Body.Close() }()
	extension := ".jpg"
	if cleaned.ContentType == "image/png" {
		extension = ".png"
	}
	key := "avatars/" + uuid.NewString() + extension
	if err = u.storage.Put(ctx, key, cleaned.Body, cleaned.Size, cleaned.ContentType); err != nil {
		return domain.User{}, err
	}
	user, oldKey, err := u.users.SwapAvatar(ctx, id, key)
	if err != nil {
		// Неудачный COMMIT мог на самом деле пройти. Не удаляем объект, на который может быть ссылка.
		return domain.User{}, err
	}
	if oldKey != "" && oldKey != key {
		if err = u.storage.Delete(ctx, oldKey); err != nil {
			slog.WarnContext(ctx, "avatar.cleanup", slog.String("outcome", "failure"))
		}
	}
	return user, nil
}
