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

// AuthState is private authentication state, available only under the user row lock.
type AuthState struct {
	User         domain.User
	PasswordHash string
	Secret       []byte
	LastStep     int64
}

// TwoFactorSetup contains sensitive setup data and must never be logged.
type TwoFactorSetup struct{ QRCode, Secret string }

// TwoFactorSecurity isolates encryption, TOTP and QR rendering from scenarios.
type TwoFactorSecurity interface {
	NewSecret() (string, error)
	Setup(secret, login string) (TwoFactorSetup, error)
	Encrypt(userID int64, secret string) ([]byte, error)
	Decrypt(userID int64, ciphertext []byte) (string, error)
	Match(secret, code string, now time.Time) (step int64, valid bool)
}

// AttemptLimiter atomically admits attempts across API instances; errors fail closed.
type AttemptLimiter interface {
	Allow(ctx context.Context, key string, limit int) error
}

// WithTwoFactor wires mandatory production authentication safeguards.
func WithTwoFactor(security TwoFactorSecurity, limiter AttemptLimiter) func(*UserUsecase) {
	return func(uc *UserUsecase) { uc.twoFactor = security; uc.limiter = limiter }
}
