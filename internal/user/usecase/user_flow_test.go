package usecase

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Эти сценарии проверяют ротацию токенов и logout на in-memory хранилище сессий.
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
			calls := 1
			if tt.wantErr == nil {
				calls = 2
			}
			uc := NewUserUsecase(repo, repo, flowIssuer(t, calls), flowPasswords{}, flowRefresh{}, refreshTTL)
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
	uc := NewUserUsecase(repo, repo, flowIssuer(t, 1), flowPasswords{}, flowRefresh{}, refreshTTL)
	_, sess, err := uc.Login(context.Background(), "alice", "password123")
	if err != nil {
		t.Fatal(err)
	}

	if err := uc.Logout(context.Background(), sess.RefreshToken, sess.SessionClaims); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := uc.Refresh(context.Background(), sess.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Errorf("Refresh() после Logout: error = %v, want ErrInvalidRefresh", err)
	}
	if err := uc.Logout(context.Background(), sess.RefreshToken, sess.SessionClaims); !errors.Is(err, ErrInvalidRefresh) {
		t.Errorf("повторный Logout(): error = %v, want ErrInvalidRefresh", err)
	}
}
