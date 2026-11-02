package usecase

import (
	"context"
	"regexp"
	"unicode/utf8"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
)

var profileLogin = regexp.MustCompile(`^[a-zA-Z0-9_]{3,32}$`)

func (uc *UserUsecase) UpdateCurrentUser(ctx context.Context, id int64, login string) (domain.User, error) {
	if !profileLogin.MatchString(login) {
		return domain.User{}, ErrValidation
	}
	return uc.users.UpdateLogin(ctx, id, login)
}

func (uc *UserUsecase) ChangeCurrentUserPassword(ctx context.Context, id int64, currentPassword, newPassword string) error {
	if currentPassword == newPassword || !validProfilePassword(newPassword) || !validProfilePassword(currentPassword) {
		return ErrValidation
	}
	_, oldHash, err := uc.users.CredentialsByID(ctx, id)
	if err != nil {
		return err
	}
	matched, err := uc.passwords.Compare(oldHash, currentPassword)
	if err != nil {
		return err
	}
	if !matched {
		return ErrInvalidCurrentPassword
	}
	newHash, err := uc.passwords.Hash(newPassword)
	if err != nil {
		return err
	}
	if err = uc.users.CompareAndSwapPassword(ctx, id, oldHash, newHash); err != nil {
		return err
	}
	return uc.RevokeAll(ctx, id)
}

func validProfilePassword(password string) bool {
	return utf8.ValidString(password) && utf8.RuneCountInString(password) >= 8 && utf8.RuneCountInString(password) <= 72 && len(password) <= 72
}
