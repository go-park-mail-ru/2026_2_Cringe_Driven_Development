package user

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/auth"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrLoginTaken = errors.New("login is taken")
	// ErrInvalidCredentials — и для неверного логина, и для неверного пароля:
	// по ответу нельзя узнать, существует ли логин.
	ErrInvalidCredentials = errors.New("invalid login or password")
	// ErrPasswordTooLong: bcrypt считает байты, а контракт — символы.
	// 72 символа кириллицей — это 144 байта.
	ErrPasswordTooLong = errors.New("password is longer than 72 bytes")
	ErrInvalidRefresh  = errors.New("refresh token is invalid or expired")
	ErrUserNotFound    = errors.New("user not found")
	ErrSessionNotFound = errors.New("refresh session not found")
)

type User struct {
	ID    uuid.UUID
	Login string
}

// Session — то, что получает клиент после входа или refresh.
type Session struct {
	UserID       uuid.UUID
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// repository — интерфейс, а не *Repository, чтобы в тестах сервиса
// подставлять подмену без базы.
type repository interface {
	CreateUser(ctx context.Context, login, passwordHash string) (User, error)
	UserByLogin(ctx context.Context, login string) (User, string, error)
	UserByID(ctx context.Context, id uuid.UUID) (User, error)
	CreateSession(ctx context.Context, userID uuid.UUID, tokenHash string, expiresAt time.Time) error
	DeleteSession(ctx context.Context, tokenHash string) (uuid.UUID, time.Time, error)
}

type tokenIssuer interface {
	Issue(userID uuid.UUID) (string, error)
}

type Service struct {
	repo       repository
	tokens     tokenIssuer
	refreshTTL time.Duration
	// now подменяется в тестах, чтобы проверить истёкший токен.
	now func() time.Time
}

func NewService(repo repository, tokens tokenIssuer, refreshTTL time.Duration) *Service {
	return &Service{repo: repo, tokens: tokens, refreshTTL: refreshTTL, now: time.Now}
}

func (s *Service) Register(ctx context.Context, login, password string) (User, Session, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if errors.Is(err, bcrypt.ErrPasswordTooLong) {
		return User{}, Session{}, ErrPasswordTooLong
	}
	if err != nil {
		return User{}, Session{}, fmt.Errorf("hash password: %w", err)
	}
	u, err := s.repo.CreateUser(ctx, login, string(hash))
	if err != nil {
		return User{}, Session{}, err
	}
	sess, err := s.startSession(ctx, u.ID)
	return u, sess, err
}

func (s *Service) Login(ctx context.Context, login, password string) (User, Session, error) {
	u, hash, err := s.repo.UserByLogin(ctx, login)
	if errors.Is(err, ErrUserNotFound) {
		return User{}, Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, Session{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return User{}, Session{}, ErrInvalidCredentials
	}
	sess, err := s.startSession(ctx, u.ID)
	return u, sess, err
}

// Refresh — ротация: старая сессия удаляется, вместо неё создаётся новая.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (Session, error) {
	userID, expiresAt, err := s.repo.DeleteSession(ctx, auth.HashRefreshToken(refreshToken))
	if errors.Is(err, ErrSessionNotFound) {
		return Session{}, ErrInvalidRefresh
	}
	if err != nil {
		return Session{}, err
	}
	if !s.now().Before(expiresAt) {
		return Session{}, ErrInvalidRefresh
	}
	return s.startSession(ctx, userID)
}

func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	_, _, err := s.repo.DeleteSession(ctx, auth.HashRefreshToken(refreshToken))
	if errors.Is(err, ErrSessionNotFound) {
		return ErrInvalidRefresh
	}
	return err
}

func (s *Service) CurrentUser(ctx context.Context, id uuid.UUID) (User, error) {
	return s.repo.UserByID(ctx, id)
}

func (s *Service) startSession(ctx context.Context, userID uuid.UUID) (Session, error) {
	access, err := s.tokens.Issue(userID)
	if err != nil {
		return Session{}, err
	}
	refresh, hash := auth.NewRefreshToken()
	expiresAt := s.now().Add(s.refreshTTL)
	if err := s.repo.CreateSession(ctx, userID, hash, expiresAt); err != nil {
		return Session{}, err
	}
	return Session{UserID: userID, AccessToken: access, RefreshToken: refresh, ExpiresAt: expiresAt}, nil
}
