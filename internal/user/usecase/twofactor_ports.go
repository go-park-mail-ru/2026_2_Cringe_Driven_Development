package usecase

import (
	"context"
	"errors"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
)

var (
	ErrTwoFactorRequired = errors.New("TOTP code required")
	ErrInvalidTOTP       = errors.New("invalid or already used TOTP code")
	ErrRateLimited       = errors.New("too many requests")
)

// AuthState содержит приватное состояние аутентификации и доступен только под блокировкой строки пользователя.
type AuthState struct {
	User         domain.User
	PasswordHash string
	Secret       []byte
	LastStep     int64
}

// TwoFactorSetup содержит чувствительные данные настройки и никогда не должен логироваться.
type TwoFactorSetup struct{ QRCode, Secret string }

// TwoFactorSecurity изолирует шифрование, TOTP и рендеринг QR от сценариев.
type TwoFactorSecurity interface {
	NewSecret() (string, error)
	Setup(secret, login string) (TwoFactorSetup, error)
	Encrypt(userID int64, secret string) ([]byte, error)
	Decrypt(userID int64, ciphertext []byte) (string, error)
	Match(secret, code string, now time.Time) (step int64, valid bool)
}

// AttemptLimiter атомарно учитывает попытки на всех инстансах API и при ошибке отказывает.
type AttemptLimiter interface {
	Allow(ctx context.Context, key string, limit int) error
}

// WithTwoFactor подключает обязательные продовые защиты аутентификации.
func WithTwoFactor(security TwoFactorSecurity, limiter AttemptLimiter) func(*UserUsecase) {
	return func(uc *UserUsecase) { uc.twoFactor = security; uc.limiter = limiter }
}
