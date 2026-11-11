package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

func accountAttemptKey(operation string, id int64) string {
	return operation + ":" + strconv.FormatInt(id, 10)
}
func unknownLoginKey(login string) string {
	hash := sha256.Sum256([]byte(strings.ToLower(login)))
	return "login_unknown:" + hex.EncodeToString(hash[:])
}
func (uc *UserUsecase) allow(ctx context.Context, key string, limit int) error {
	if uc.limiter == nil {
		return nil
	}
	return uc.limiter.Allow(ctx, key, limit)
}

// EnableTwoFactor готовит стабильный ожидающий секрет, не включая аутентификацию.
func (uc *UserUsecase) EnableTwoFactor(ctx context.Context, id int64) (TwoFactorSetup, error) {
	if err := uc.allow(ctx, accountAttemptKey("setup", id), 30); err != nil {
		return TwoFactorSetup{}, err
	}
	if uc.twoFactor == nil {
		return TwoFactorSetup{}, errors.New("TOTP security is not configured")
	}
	var result TwoFactorSetup
	err := uc.users.WithAuthLock(ctx, id, func(state *AuthState) error {
		if state.User.Has2FA {
			return ErrValidation
		}
		var secret string
		var err error
		if len(state.Secret) == 0 {
			secret, err = uc.twoFactor.NewSecret()
			if err != nil {
				return err
			}
			state.Secret, err = uc.twoFactor.Encrypt(id, secret)
			state.LastStep = -1
		} else {
			secret, err = uc.twoFactor.Decrypt(id, state.Secret)
		}
		if err != nil {
			return err
		}
		result, err = uc.twoFactor.Setup(secret, state.User.Login)
		return err
	})
	if err != nil {
		return TwoFactorSetup{}, err
	}
	return result, nil
}

// ConfirmTwoFactor активирует подготовленный секрет и расходует принятый шаг.
func (uc *UserUsecase) ConfirmTwoFactor(ctx context.Context, id int64, code string) error {
	if err := uc.allow(ctx, accountAttemptKey("confirm", id), 5); err != nil {
		return err
	}
	if !validTOTPFormat(code) {
		return ErrValidation
	}
	return uc.users.WithAuthLock(ctx, id, func(state *AuthState) error {
		if state.User.Has2FA || len(state.Secret) == 0 {
			return ErrValidation
		}
		if err := uc.consumeTOTP(state, code); err != nil {
			return err
		}
		state.User.Has2FA = true
		return nil
	})
}

// DisableTwoFactor удаляет секрет после проверки текущего пароля.
func (uc *UserUsecase) DisableTwoFactor(ctx context.Context, id int64, password string) error {
	if err := uc.allow(ctx, accountAttemptKey("disable", id), 5); err != nil {
		return err
	}
	return uc.users.WithAuthLock(ctx, id, func(state *AuthState) error {
		if !state.User.Has2FA {
			return ErrValidation
		}
		matched, err := uc.passwords.Compare(state.PasswordHash, password)
		if err != nil {
			return err
		}
		if !matched {
			return ErrInvalidCurrentPassword
		}
		state.User.Has2FA = false
		state.Secret = nil
		state.LastStep = -1
		return nil
	})
}

func validTOTPFormat(code string) bool {
	if len(code) != 6 {
		return false
	}
	for i := range len(code) {
		if code[i] < '0' || code[i] > '9' {
			return false
		}
	}
	return true
}
func (uc *UserUsecase) consumeTOTP(state *AuthState, code string) error {
	if !validTOTPFormat(code) {
		return ErrInvalidTOTP
	}
	if uc.twoFactor == nil {
		return errors.New("TOTP security is not configured")
	}
	secret, err := uc.twoFactor.Decrypt(state.User.ID, state.Secret)
	if err != nil {
		return err
	}
	step, valid := uc.twoFactor.Match(secret, code, uc.now())
	if !valid || step <= state.LastStep {
		return ErrInvalidTOTP
	}
	state.LastStep = step
	return nil
}
