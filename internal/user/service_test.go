package user

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/auth"
)

const refreshTTL = time.Hour

func TestServiceRegister(t *testing.T) {
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
			svc := NewService(repo, fakeTokens{}, refreshTTL)

			u, sess, err := svc.Register(context.Background(), tt.login, tt.password)
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

func TestServiceLogin(t *testing.T) {
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
			svc := NewService(repo, fakeTokens{}, refreshTTL)

			u, _, err := svc.Login(context.Background(), tt.login, tt.password)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Login() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && u != want {
				t.Errorf("Login() = %v, want %v", u, want)
			}
		})
	}
}

func TestServiceRefresh(t *testing.T) {
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
			svc := NewService(repo, fakeTokens{}, refreshTTL)
			_, first, err := svc.Login(context.Background(), "alice", "password123")
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			svc.now = func() time.Time { return start.Add(tt.after) }

			second, err := svc.Refresh(context.Background(), tt.token(first.RefreshToken))
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
			if _, err := svc.Refresh(context.Background(), first.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
				t.Errorf("повторный Refresh() старым токеном: error = %v, want ErrInvalidRefresh", err)
			}
		})
	}
}

func TestServiceLogout(t *testing.T) {
	repo := newFakeRepo()
	repo.addUser("alice", "password123")
	svc := NewService(repo, fakeTokens{}, refreshTTL)
	_, sess, err := svc.Login(context.Background(), "alice", "password123")
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Logout(context.Background(), sess.RefreshToken); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := svc.Refresh(context.Background(), sess.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Errorf("Refresh() после Logout: error = %v, want ErrInvalidRefresh", err)
	}
	if err := svc.Logout(context.Background(), sess.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Errorf("повторный Logout(): error = %v, want ErrInvalidRefresh", err)
	}
}
