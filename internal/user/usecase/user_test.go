package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"go.uber.org/mock/gomock"
)

const refreshTTL = time.Hour

func expectSession(ctx context.Context, f userFixture, authorized *gomock.Call) {
	gomock.InOrder(

		f.tokens.EXPECT().IssueAccess(gomock.Any()).Return("access-SECRET", f.now.Add(time.Minute), nil).After(authorized),
		f.refresh.EXPECT().New().Return("refresh-SECRET", "hash-SECRET"),
		f.sessions.EXPECT().Create(ctx, gomock.Any()).Return(nil),
	)
}

func TestUserUsecaseRegister(t *testing.T) {
	failure := errors.New("db unavailable")
	for _, tc := range []struct {
		name, login, password string
		want                  error
	}{
		{"успех", "alice", `a<&>"'12345`, nil},
		{"логин занят в другом регистре", "BOB", "password123", ErrLoginTaken},
		{"пароль не ASCII", "cyr", strings.Repeat("я", 8), ErrValidation},
		{"пароль с пробелом", "alice", "pass word123", ErrValidation},
		{"ошибка хранилища", "alice", "password123", failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUserFixture(t)
			type requestKey struct{}
			ctx := context.WithValue(context.Background(), requestKey{}, tc.name)
			want := domain.User{ID: 7, Login: tc.login}
			if !errors.Is(tc.want, ErrValidation) {
				hash := f.passwords.EXPECT().Hash(tc.password).Return("password-hash", nil)
				created := f.users.EXPECT().CreateUser(ctx, tc.login, "password-hash").Return(want, tc.want).After(hash)
				if tc.want == nil {
					version := f.sessions.EXPECT().CurrentVersion(ctx, int64(7)).Return(SessionVersion(1), nil).After(created)
					credentials := f.users.EXPECT().CredentialsByID(ctx, int64(7)).Return(want, "password-hash", nil).After(version)
					verified := f.passwords.EXPECT().Compare("password-hash", tc.password).Return(true, nil).After(credentials)
					expectSession(ctx, f, verified)
				}
			}
			u, sess, err := f.uc.Register(ctx, tc.login, tc.password)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
			if tc.want == nil && (u != want || sess != (Session{SessionClaims: sess.SessionClaims, AccessExpiresAt: f.now.Add(time.Minute), AccessToken: "access-SECRET", RefreshToken: "refresh-SECRET", ExpiresAt: f.now.Add(refreshTTL)})) {
				t.Fatalf("user=%+v session=%+v", u, sess)
			}
		})
	}
}

func TestUserUsecaseLogin(t *testing.T) {
	failure := errors.New("store failed")
	for _, tc := range []struct {
		name, login string
		storeErr    error
		matched     bool
		want        error
	}{
		{"успех", "alice", nil, true, nil}, {"логин в другом регистре", "Alice", nil, true, nil},
		{"неверный пароль", "alice", nil, false, ErrInvalidCredentials},
		{"нет такого логина", "nobody", ErrUserNotFound, false, ErrInvalidCredentials},
		{"ошибка хранилища", "alice", failure, false, failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUserFixture(t)
			ctx := context.Background()
			want := domain.User{ID: 7, Login: "alice"}
			lookup := f.users.EXPECT().UserByLogin(ctx, tc.login).Return(want, "password-hash", tc.storeErr)
			if tc.storeErr == nil {
				version := f.sessions.EXPECT().CurrentVersion(ctx, int64(7)).Return(SessionVersion(1), nil).After(lookup)
				credentials := f.users.EXPECT().CredentialsByID(ctx, int64(7)).Return(want, "password-hash", nil).After(version)
				verified := f.passwords.EXPECT().Compare("password-hash", "password123").Return(tc.matched, nil).After(credentials)
				if tc.matched {
					expectSession(ctx, f, verified)
				}
			}
			u, _, err := f.uc.Login(ctx, tc.login, "password123")
			if !errors.Is(err, tc.want) || (tc.want == nil && u != want) {
				t.Fatalf("user=%v error=%v", u, err)
			}
		})
	}
}

func TestUserUsecaseCurrentUser(t *testing.T) {
	for _, wantErr := range []error{nil, ErrUserNotFound, errors.New("db failed")} {
		t.Run(errorName(wantErr), func(t *testing.T) {
			f := newUserFixture(t)
			ctx := context.Background()
			want := domain.User{ID: 7, Login: "alice"}
			f.users.EXPECT().UserByID(ctx, int64(7)).Return(want, wantErr)
			got, err := f.uc.CurrentUser(ctx, 7)
			if !errors.Is(err, wantErr) || (wantErr == nil && got != want) {
				t.Fatalf("got=%v err=%v", got, err)
			}
		})
	}
}

func errorName(err error) string {
	if err == nil {
		return "success"
	}
	return err.Error()
}
