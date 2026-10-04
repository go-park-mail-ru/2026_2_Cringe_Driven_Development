package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/auth"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/models"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/repository"
	"golang.org/x/crypto/bcrypt"
)

type UserUsecase struct {
	repo       repository.Repository
	tokens     TokenIssuer
	refreshTTL time.Duration
	// now подменяется в тестах, чтобы проверить истёкший токен.
	now func() time.Time
}

var _ Usecase = (*UserUsecase)(nil)

func NewUserUsecase(repo repository.Repository, tokens TokenIssuer, refreshTTL time.Duration) *UserUsecase {
	return &UserUsecase{repo: repo, tokens: tokens, refreshTTL: refreshTTL, now: time.Now}
}

func (uc *UserUsecase) Register(ctx context.Context, login, password string) (models.User, models.Session, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if errors.Is(err, bcrypt.ErrPasswordTooLong) {
		return models.User{}, models.Session{}, ErrPasswordTooLong
	}
	if err != nil {
		return models.User{}, models.Session{}, fmt.Errorf("hash password: %w", err)
	}
	u, err := uc.repo.CreateUser(ctx, login, string(hash))
	if errors.Is(err, repository.ErrLoginTaken) {
		return models.User{}, models.Session{}, ErrLoginTaken
	}
	if err != nil {
		return models.User{}, models.Session{}, err
	}
	sess, err := uc.startSession(ctx, u.ID)
	return u, sess, err
}

func (uc *UserUsecase) Login(ctx context.Context, login, password string) (models.User, models.Session, error) {
	u, hash, err := uc.repo.UserByLogin(ctx, login)
	if errors.Is(err, repository.ErrUserNotFound) {
		return models.User{}, models.Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return models.User{}, models.Session{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return models.User{}, models.Session{}, ErrInvalidCredentials
	}
	sess, err := uc.startSession(ctx, u.ID)
	return u, sess, err
}

func (uc *UserUsecase) Refresh(ctx context.Context, refreshToken string) (models.Session, error) {
	userID, expiresAt, err := uc.repo.DeleteSession(ctx, auth.HashRefreshToken(refreshToken))
	if errors.Is(err, repository.ErrSessionNotFound) {
		return models.Session{}, ErrInvalidRefresh
	}
	if err != nil {
		return models.Session{}, err
	}
	if !uc.now().Before(expiresAt) {
		return models.Session{}, ErrInvalidRefresh
	}
	return uc.startSession(ctx, userID)
}

func (uc *UserUsecase) Logout(ctx context.Context, refreshToken string) error {
	_, _, err := uc.repo.DeleteSession(ctx, auth.HashRefreshToken(refreshToken))
	if errors.Is(err, repository.ErrSessionNotFound) {
		return ErrInvalidRefresh
	}
	return err
}

func (uc *UserUsecase) CurrentUser(ctx context.Context, id int64) (models.User, error) {
	u, err := uc.repo.UserByID(ctx, id)
	if errors.Is(err, repository.ErrUserNotFound) {
		return models.User{}, ErrUserNotFound
	}
	return u, err
}

func (uc *UserUsecase) startSession(ctx context.Context, userID int64) (models.Session, error) {
	access, err := uc.tokens.Issue(userID)
	if err != nil {
		return models.Session{}, err
	}
	refresh, hash := auth.NewRefreshToken()
	expiresAt := uc.now().Add(uc.refreshTTL)
	if err := uc.repo.CreateSession(ctx, userID, hash, expiresAt); err != nil {
		return models.Session{}, err
	}
	return models.Session{UserID: userID, AccessToken: access, RefreshToken: refresh, ExpiresAt: expiresAt}, nil
}
