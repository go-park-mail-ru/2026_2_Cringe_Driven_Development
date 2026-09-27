package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/auth"
	"github.com/google/uuid"
)

const refreshTTL = time.Hour

func TestUserUsecaseRegister(t *testing.T) {
	tests := []struct {
		name     string
		login    string
		password string
		wantErr  error
	}{
		{name: "успех", login: "alice", password: "password123"},
		{name: "логин занят в другом регистре", login: "BOB", password: "password123", wantErr: ErrLoginTaken},
		{name: "пароль длиннее 72 байт", login: "cyr", password: strings.Repeat("я", 72), wantErr: ErrPasswordTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			repo.addUser("bob", "password123")
			uc := NewUserUsecase(repo, fakeTokens{}, refreshTTL)

			u, sess, err := uc.Register(context.Background(), tt.login, tt.password)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Register() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if u.Login != tt.login {
				t.Errorf("login = %q, want %q", u.Login, tt.login)
			}
			if sess.AccessToken == "" || sess.RefreshToken == "" {
				t.Error("после регистрации нет токенов")
			}
			if _, ok := repo.sessions[auth.HashRefreshToken(sess.RefreshToken)]; !ok {
				t.Error("сессия не сохранена по хешу токена")
			}
		})
	}
}

func TestUserUsecaseLogin(t *testing.T) {
	tests := []struct {
		name     string
		login    string
		password string
		wantErr  error
	}{
		{name: "успех", login: "alice", password: "password123"},
		{name: "логин в другом регистре", login: "Alice", password: "password123"},
		{name: "неверный пароль", login: "alice", password: "wrong-password", wantErr: ErrInvalidCredentials},
		{name: "нет такого логина", login: "nobody", password: "password123", wantErr: ErrInvalidCredentials},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			want := repo.addUser("alice", "password123")
			uc := NewUserUsecase(repo, fakeTokens{}, refreshTTL)

			u, _, err := uc.Login(context.Background(), tt.login, tt.password)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Login() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && u != want {
				t.Errorf("Login() = %v, want %v", u, want)
			}
		})
	}
}

func TestUserUsecaseRefresh(t *testing.T) {
	tests := []struct {
		name    string
		token   func(issued string) string
		after   time.Duration
		wantErr error
	}{
		{name: "успех", token: func(issued string) string { return issued }},
		{name: "истёкший", token: func(issued string) string { return issued }, after: 2 * refreshTTL, wantErr: ErrInvalidRefresh},
		{name: "чужой токен", token: func(string) string { return "not-issued-by-us" }, wantErr: ErrInvalidRefresh},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			repo.addUser("alice", "password123")
			uc := NewUserUsecase(repo, fakeTokens{}, refreshTTL)
			_, first, err := uc.Login(context.Background(), "alice", "password123")
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			uc.now = func() time.Time { return start.Add(tt.after) }

			second, err := uc.Refresh(context.Background(), tt.token(first.RefreshToken))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Refresh() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if second.RefreshToken == first.RefreshToken {
				t.Error("refresh вернул тот же токен")
			}
			// Ротация: старым токеном второй раз обновиться нельзя.
			if _, err := uc.Refresh(context.Background(), first.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
				t.Errorf("повторный Refresh() старым токеном: error = %v, want ErrInvalidRefresh", err)
			}
		})
	}
}

func TestUserUsecaseLogout(t *testing.T) {
	repo := newFakeRepo()
	repo.addUser("alice", "password123")
	uc := NewUserUsecase(repo, fakeTokens{}, refreshTTL)
	_, sess, err := uc.Login(context.Background(), "alice", "password123")
	if err != nil {
		t.Fatal(err)
	}

	if err := uc.Logout(context.Background(), sess.RefreshToken); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := uc.Refresh(context.Background(), sess.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Errorf("Refresh() после Logout: error = %v, want ErrInvalidRefresh", err)
	}
	if err := uc.Logout(context.Background(), sess.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Errorf("повторный Logout(): error = %v, want ErrInvalidRefresh", err)
	}
}

func TestUserUsecaseCurrentUser(t *testing.T) {
	repo := newFakeRepo()
	want := repo.addUser("alice", "password123")
	uc := NewUserUsecase(repo, fakeTokens{}, refreshTTL)

	got, err := uc.CurrentUser(context.Background(), want.ID)
	if err != nil || got != want {
		t.Errorf("CurrentUser() = %v, %v; want %v, nil", got, err, want)
	}
	if _, err := uc.CurrentUser(context.Background(), uuid.New()); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("CurrentUser() неизвестного: error = %v, want ErrUserNotFound", err)
	}
}
