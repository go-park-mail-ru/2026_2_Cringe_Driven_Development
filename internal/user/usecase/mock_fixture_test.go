package usecase

import (
	"context"
	"testing"
	"time"

	"go.uber.org/mock/gomock"
)

type userFixture struct {
	uc        *UserUsecase
	users     *MockUserStore
	sessions  *MockSessionStore
	tokens    *MockTokenIssuer
	passwords *MockPasswordHasher
	refresh   *MockRefreshTokens
	now       time.Time
}

func newUserFixture(t *testing.T) userFixture {
	t.Helper()
	ctrl := gomock.NewController(t)
	f := userFixture{users: NewMockUserStore(ctrl), sessions: NewMockSessionStore(ctrl), tokens: NewMockTokenIssuer(ctrl), passwords: NewMockPasswordHasher(ctrl), refresh: NewMockRefreshTokens(ctrl), now: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	f.uc = NewUserUsecase(f.users, f.sessions, f.tokens, f.passwords, f.refresh, refreshTTL)
	f.users.EXPECT().WithAuthLock(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, id int64, fn func(*AuthState) error) error {
		u, hash, err := f.users.CredentialsByID(ctx, id)
		if err != nil {
			return err
		}
		return fn(&AuthState{User: u, PasswordHash: hash, LastStep: -1})
	}).AnyTimes()
	f.uc.now = func() time.Time { return f.now }
	return f
}
