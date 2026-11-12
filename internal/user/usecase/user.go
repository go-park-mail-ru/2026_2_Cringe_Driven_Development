package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/observability"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"github.com/google/uuid"
)

type UserUsecase struct {
	twoFactor  TwoFactorSecurity
	limiter    AttemptLimiter
	users      UserStore
	sessions   SessionStore
	tokens     TokenIssuer
	passwords  PasswordHasher
	refresh    RefreshTokens
	refreshTTL time.Duration
	// now подменяется в тестах, чтобы проверить истёкший токен.
	now func() time.Time
}

func NewUserUsecase(users UserStore, sessions SessionStore, tokens TokenIssuer, passwords PasswordHasher, refresh RefreshTokens, refreshTTL time.Duration, options ...func(*UserUsecase)) *UserUsecase {
	uc := &UserUsecase{users: users, sessions: sessions, tokens: tokens, passwords: passwords, refresh: refresh, refreshTTL: refreshTTL, now: time.Now}
	for _, option := range options {
		option(uc)
	}
	return uc
}

func (uc *UserUsecase) Register(ctx context.Context, login, password string) (domain.User, Session, error) {
	if !passwordRule.MatchString(password) {
		return domain.User{}, Session{}, ErrValidation
	}
	hash, err := uc.passwords.Hash(password)
	if err != nil {
		return domain.User{}, Session{}, observability.WithCode(fmt.Errorf("hash password: %w", err), "password_hash_failed")
	}
	u, err := uc.users.CreateUser(ctx, login, string(hash))
	if errors.Is(err, ErrLoginTaken) {
		return domain.User{}, Session{}, ErrLoginTaken
	}
	if err != nil {
		return domain.User{}, Session{}, err
	}
	// Фиксируем барьер отзыва до финального чтения пароля. Create должен
	// отклонить эту версию, если конкурентная смена пароля отзывает сессии.
	version, err := uc.sessions.CurrentVersion(ctx, u.ID)
	if err != nil {
		return u, Session{}, err
	}
	current, currentHash, err := uc.users.CredentialsByID(ctx, u.ID)
	if err != nil {
		return u, Session{}, err
	}
	matched, err := uc.passwords.Compare(currentHash, password)
	if err != nil {
		return u, Session{}, err
	}
	if !matched {
		return u, Session{}, ErrVersionChanged
	}
	u = current
	sess, err := uc.startSession(ctx, u.ID, version)
	if err == nil {
		slog.InfoContext(ctx, "user.register", slog.String("outcome", "success"), slog.Int64("user_id", u.ID))
	}
	return u, sess, err
}

func (uc *UserUsecase) Login(ctx context.Context, login, password string, userCode ...*string) (domain.User, Session, error) {
	u, _, err := uc.users.UserByLogin(ctx, login)
	if errors.Is(err, ErrUserNotFound) {
		if limitErr := uc.allow(ctx, unknownLoginKey(login), 10); limitErr != nil {
			return domain.User{}, Session{}, limitErr
		}
		return domain.User{}, Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return domain.User{}, Session{}, err
	}
	if err = uc.allow(ctx, accountAttemptKey("login", u.ID), 10); err != nil {
		return domain.User{}, Session{}, err
	}
	version, err := uc.sessions.CurrentVersion(ctx, u.ID)
	if err != nil {
		return domain.User{}, Session{}, err
	}
	var sess Session
	err = uc.users.WithAuthLock(ctx, u.ID, func(state *AuthState) error {
		matched, compareErr := uc.passwords.Compare(state.PasswordHash, password)
		if compareErr != nil {
			return observability.WithCode(fmt.Errorf("compare password: %w", compareErr), "password_compare_failed")
		}
		if !matched {
			return ErrInvalidCredentials
		}
		if state.User.Has2FA {
			if len(userCode) == 0 || userCode[0] == nil {
				return ErrTwoFactorRequired
			}
			if checkErr := uc.consumeTOTP(state, *userCode[0]); checkErr != nil {
				return checkErr
			}
		}
		u = state.User
		sess, err = uc.startSession(ctx, u.ID, version)
		return err
	})
	if errors.Is(err, ErrVersionChanged) || errors.Is(err, ErrUserNotFound) {
		err = ErrInvalidCredentials
	}
	if err != nil {
		return domain.User{}, Session{}, err
	}
	slog.InfoContext(ctx, "user.login", slog.String("outcome", "success"), slog.Int64("user_id", u.ID))
	return u, sess, nil
}

func (uc *UserUsecase) Refresh(ctx context.Context, refreshToken string) (Session, error) {
	oldHash := uc.refresh.Hash(refreshToken)
	old, err := uc.sessions.Find(ctx, oldHash)
	if err != nil {
		return Session{}, refreshError(err)
	}
	if !uc.now().Before(old.ExpiresAt) {
		return Session{}, ErrInvalidRefresh
	}
	sess, replacement, err := uc.prepareSession(old.SessionClaims)
	if err != nil {
		return Session{}, err
	}
	if err = uc.sessions.Rotate(ctx, oldHash, replacement); err != nil {
		return Session{}, refreshError(err)
	}
	slog.InfoContext(ctx, "session.refresh", slog.String("outcome", "success"), slog.Int64("user_id", old.UserID))
	return sess, nil
}

func refreshError(err error) error {
	if errors.Is(err, ErrInvalidSession) {
		return ErrInvalidRefresh
	}
	return err
}

func (uc *UserUsecase) Logout(ctx context.Context, refreshToken string, claims SessionClaims) error {
	err := uc.sessions.Revoke(ctx, uc.refresh.Hash(refreshToken), claims)
	if err == nil {
		slog.InfoContext(ctx, "session.logout", slog.String("outcome", "success"))
	}
	return refreshError(err)
}

// RevokeAll инвалидирует ранее выданную авторизацию.
func (uc *UserUsecase) RevokeAll(ctx context.Context, userID int64) error {
	return uc.sessions.RevokeAll(ctx, userID)
}

func (uc *UserUsecase) CurrentUser(ctx context.Context, id int64) (domain.User, error) {
	u, err := uc.users.UserByID(ctx, id)
	if errors.Is(err, ErrUserNotFound) {
		return domain.User{}, ErrUserNotFound
	}
	return u, err
}

func (uc *UserUsecase) startSession(ctx context.Context, userID int64, version SessionVersion) (Session, error) {
	sid, err := uuid.NewRandom()
	if err != nil {
		return Session{}, err
	}
	sess, replacement, err := uc.prepareSession(SessionClaims{UserID: userID, SessionID: sid.String(), Version: version})
	if err != nil {
		return Session{}, err
	}
	if err = uc.sessions.Create(ctx, replacement); err != nil {
		return Session{}, err
	}
	return sess, nil
}

func (uc *UserUsecase) prepareSession(claims SessionClaims) (Session, NewSession, error) {
	access, accessExpiresAt, err := uc.tokens.IssueAccess(claims)
	if err != nil {
		return Session{}, NewSession{}, err
	}
	refresh, hash := uc.refresh.New()
	expiresAt := uc.now().Add(uc.refreshTTL)
	return Session{SessionClaims: claims, AccessToken: access, AccessExpiresAt: accessExpiresAt, RefreshToken: refresh, ExpiresAt: expiresAt},
		NewSession{RefreshSession: RefreshSession{SessionClaims: claims, TokenHash: hash, ExpiresAt: expiresAt}, AccessExpiresAt: accessExpiresAt}, nil
}
