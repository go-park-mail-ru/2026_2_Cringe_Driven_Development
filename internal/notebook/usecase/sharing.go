package usecase

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

var readerLogin = regexp.MustCompile(`^[A-Za-z0-9_]{3,32}$`)

// NormalizeName сохраняет общие правила создания, переименования и копирования.
func NormalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrEmptyName
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 128 || strings.ContainsRune(name, 0) {
		return "", ErrInvalidName
	}
	return name, nil
}

func ValidateSharing(mode domain.SharingMode, logins []string) error {
	switch mode {
	case domain.SharingModePrivate, domain.SharingModePublic:
		if len(logins) != 0 {
			return ErrInvalidSharing
		}
	case domain.SharingModeRestricted:
	default:
		return ErrInvalidSharing
	}
	for _, login := range logins {
		if !readerLogin.MatchString(login) {
			return ErrInvalidSharing
		}
	}
	return nil
}

func (uc *NotebookUsecase) GetSharing(ctx context.Context, id, ownerID int64) (domain.SharingSettings, error) {
	return uc.repo.GetSharing(ctx, id, ownerID)
}
func (uc *NotebookUsecase) PutSharing(ctx context.Context, id, ownerID int64, mode domain.SharingMode, readerLogins []string) (domain.SharingSettings, error) {
	// Проверка владельца в адаптере предшествует валидации: чужой блокнот всегда 404.
	return uc.repo.PutSharing(ctx, id, ownerID, mode, readerLogins)
}
func (uc *NotebookUsecase) Clone(ctx context.Context, id, userID int64, name string) (domain.Notebook, error) {
	if userID <= 0 {
		return domain.Notebook{}, ErrNotebookNotFound
	}
	name, err := NormalizeName(name)
	if err != nil {
		return domain.Notebook{}, err
	}
	n, err := uc.repo.Clone(ctx, id, userID, name)
	if err != nil {
		return domain.Notebook{}, err
	}
	n.Access = domain.AccessFor(n.OwnerID, userID)
	return n, nil
}

// CheckRead доступен сценариям комментариев без предоставления прав изменения.
func (uc *NotebookUsecase) CheckRead(ctx context.Context, id, userID int64) error {
	return uc.repo.CheckRead(ctx, id, userID)
}
