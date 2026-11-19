package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
)

type suggestStore struct {
	UserStore
	q     string
	limit int
}

func (s *suggestStore) SuggestUsers(_ context.Context, _ int64, q string, limit int) ([]domain.UserSuggestion, error) {
	s.q = q
	s.limit = limit
	return []domain.UserSuggestion{{ID: 2, Login: "Alice"}}, nil
}

type suggestLimiter struct {
	err   error
	key   string
	limit int
}

func (l *suggestLimiter) Allow(_ context.Context, key string, limit int) error {
	l.key = key
	l.limit = limit
	return l.err
}
func TestSuggestUsersValidationAndLimit(t *testing.T) {
	s := &suggestStore{}
	l := &suggestLimiter{}
	uc := &UserUsecase{users: s, limiter: l}
	for _, q := range []string{"", " ", "a%", "a-b", "я", "abcdefghijklmnopqrstuvwxyz1234567"} {
		if _, err := uc.SuggestUsers(t.Context(), 1, q, 10); !errors.Is(err, ErrValidation) {
			t.Fatalf("query %q: %v", q, err)
		}
	}
	for _, limit := range []int{-1, 21} {
		if _, err := uc.SuggestUsers(t.Context(), 1, "a", limit); !errors.Is(err, ErrValidation) {
			t.Fatal(err)
		}
	}
	got, err := uc.SuggestUsers(t.Context(), 1, " A_ ", 0)
	if err != nil || len(got) != 1 || s.q != "A_" || s.limit != 10 || l.limit != 60 || l.key != "user-suggest:1" {
		t.Fatalf("got=%+v err=%v store=%+v limiter=%+v", got, err, s, l)
	}
	q := strings.Repeat("a", 32)
	if _, err = uc.SuggestUsers(t.Context(), 1, " "+q+" ", 20); err != nil || s.q != q || s.limit != 20 {
		t.Fatalf("trimmed boundary query: q=%q limit=%d err=%v", s.q, s.limit, err)
	}
	l.err = ErrRateLimited
	if _, err = uc.SuggestUsers(t.Context(), 1, "a", 10); !errors.Is(err, ErrRateLimited) {
		t.Fatal(err)
	}
	uc.limiter = nil
	if _, err = uc.SuggestUsers(t.Context(), 1, "a", 10); err == nil {
		t.Fatal("missing limiter allowed suggestions")
	}
}
