package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"go.uber.org/mock/gomock"
)

func TestUpdateCurrentUserValidation(t *testing.T) {
	for _, login := range []string{"ab", strings.Repeat("a", 33), "alice!", "алиca", " alice"} {
		t.Run(login, func(t *testing.T) {
			f := newUserFixture(t)
			_, err := f.uc.UpdateCurrentUser(context.Background(), 7, login)
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	f := newUserFixture(t)
	ctx := context.Background()
	want := domain.User{ID: 7, Login: "ALIce_2"}
	f.users.EXPECT().UpdateLogin(ctx, int64(7), want.Login).Return(want, nil)
	got, err := f.uc.UpdateCurrentUser(ctx, 7, want.Login)
	if err != nil || got != want {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestChangePasswordValidation(t *testing.T) {
	for _, p := range []string{"short", strings.Repeat("a", 73), strings.Repeat("я", 37), "password123"} {
		t.Run(p, func(t *testing.T) {
			f := newUserFixture(t)
			err := f.uc.ChangeCurrentUserPassword(context.Background(), 7, "password123", p)
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestChangePasswordCASAndRevocation(t *testing.T) {
	failure := errors.New("redis unavailable")
	for _, tc := range []struct {
		name                    string
		matched                 bool
		casErr, revokeErr, want error
	}{
		{name: "success", matched: true}, {name: "incorrect", want: ErrInvalidCurrentPassword},
		{name: "concurrent change", matched: true, casErr: ErrInvalidCurrentPassword, want: ErrInvalidCurrentPassword},
		{name: "revoke failure", matched: true, revokeErr: failure, want: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUserFixture(t)
			ctx := context.Background()
			u := domain.User{ID: 7}
			calls := []any{f.users.EXPECT().CredentialsByID(ctx, int64(7)).Return(u, "oldhash", nil), f.passwords.EXPECT().Compare("oldhash", "password123").Return(tc.matched, nil)}
			if tc.matched {
				calls = append(calls, f.passwords.EXPECT().Hash("newpassword").Return("newhash", nil), f.users.EXPECT().CompareAndSwapPassword(ctx, int64(7), "oldhash", "newhash").Return(tc.casErr))
				if tc.casErr == nil {
					calls = append(calls, f.sessions.EXPECT().RevokeAll(ctx, int64(7)).Return(tc.revokeErr))
				}
			}
			gomock.InOrder(calls...)
			if err := f.uc.ChangeCurrentUserPassword(ctx, 7, "password123", "newpassword"); !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
		})
	}
}
