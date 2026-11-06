package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"go.uber.org/mock/gomock"
)

type testTwoFactor struct{ next int }

type denyAttempts struct{ err error }

func (d denyAttempts) Allow(context.Context, string, int) error { return d.err }

func TestTwoFactorLimiterFailsClosed(t *testing.T) {
	for _, failure := range []error{ErrRateLimited, errors.New("redis unavailable")} {
		f := newUserFixture(t)
		f.uc.limiter = denyAttempts{err: failure}
		f.users.EXPECT().UserByLogin(gomock.Any(), "alice").Return(domain.User{ID: 7}, "hash", nil)
		if _, pair, err := f.uc.Login(context.Background(), "alice", "password123"); !errors.Is(err, failure) || pair != (Session{}) {
			t.Fatalf("limiter bypassed: %v", err)
		}
		if _, err := f.uc.EnableTwoFactor(context.Background(), 7); !errors.Is(err, failure) {
			t.Fatal(err)
		}
		if err := f.uc.ConfirmTwoFactor(context.Background(), 7, "012345"); !errors.Is(err, failure) {
			t.Fatal(err)
		}
		if err := f.uc.DisableTwoFactor(context.Background(), 7, "password123"); !errors.Is(err, failure) {
			t.Fatal(err)
		}
	}
}

func (s *testTwoFactor) NewSecret() (string, error) { s.next++; return string(rune('A' + s.next)), nil }
func (*testTwoFactor) Setup(secret, _ string) (TwoFactorSetup, error) {
	return TwoFactorSetup{Secret: secret, QRCode: "qr:" + secret}, nil
}
func (*testTwoFactor) Encrypt(_ int64, secret string) ([]byte, error) { return []byte(secret), nil }
func (*testTwoFactor) Decrypt(_ int64, secret []byte) (string, error) { return string(secret), nil }
func (*testTwoFactor) Match(_, code string, now time.Time) (int64, bool) {
	return now.Unix() / 30, code == "012345"
}

func TestTwoFactorLifecycleAndLoginReplay(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	user := repo.addUser("alice", "password123")
	uc := NewUserUsecase(repo, repo, flowIssuer(t, 2), flowPasswords{}, flowRefresh{}, refreshTTL, WithTwoFactor(&testTwoFactor{}, nil))
	now := time.Unix(3000, 0)
	uc.now = func() time.Time { return now }
	setup, err := uc.EnableTwoFactor(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := uc.EnableTwoFactor(ctx, user.ID)
	if err != nil || repeat != setup {
		t.Fatal("setup changed", err)
	}
	u, _, err := uc.Login(ctx, "alice", "password123")
	if err != nil || u.Has2FA {
		t.Fatal("pending setup requires TOTP", err)
	}
	if err = uc.ConfirmTwoFactor(ctx, user.ID, "123456"); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatal(err)
	}
	if err = uc.ConfirmTwoFactor(ctx, user.ID, "012345"); err != nil {
		t.Fatal(err)
	}
	if _, err = uc.EnableTwoFactor(ctx, user.ID); !errors.Is(err, ErrValidation) {
		t.Fatal("active secret exposed", err)
	}
	if _, _, err = uc.Login(ctx, "alice", "wrongpassword"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal(err)
	}
	if _, _, err = uc.Login(ctx, "alice", "password123"); !errors.Is(err, ErrTwoFactorRequired) {
		t.Fatal(err)
	}
	code := "012345"
	if _, _, err = uc.Login(ctx, "alice", "password123", &code); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatal("confirmation code replayed", err)
	}
	now = now.Add(30 * time.Second)
	if u, _, err = uc.Login(ctx, "alice", "password123", &code); err != nil || !u.Has2FA {
		t.Fatal(err)
	}
	if _, _, err = uc.Login(ctx, "alice", "password123", &code); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatal("login replayed", err)
	}
	if err = uc.DisableTwoFactor(ctx, user.ID, "wrongpassword"); !errors.Is(err, ErrInvalidCurrentPassword) {
		t.Fatal(err)
	}
	if err = uc.DisableTwoFactor(ctx, user.ID, "password123"); err != nil {
		t.Fatal(err)
	}
	next, err := uc.EnableTwoFactor(ctx, user.ID)
	if err != nil || next.Secret == setup.Secret {
		t.Fatal("secret reused after disable", err)
	}
}

func TestFailedSessionCreationDoesNotConsumeTOTP(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	u := repo.addUser("alice", "password123")
	sessions := &faultSessions{fakeRepo: repo}
	uc := NewUserUsecase(repo, sessions, flowIssuer(t, 2), flowPasswords{}, flowRefresh{}, refreshTTL, WithTwoFactor(&testTwoFactor{}, nil))
	now := time.Unix(3000, 0)
	uc.now = func() time.Time { return now }
	if _, err := uc.EnableTwoFactor(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := uc.ConfirmTwoFactor(ctx, u.ID, "012345"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Second)
	code := "012345"
	failure := errors.New("session storage unavailable")
	sessions.createErr = failure
	if _, pair, err := uc.Login(ctx, "alice", "password123", &code); !errors.Is(err, failure) || pair != (Session{}) {
		t.Fatal("failed session escaped", err)
	}
	sessions.createErr = nil
	if _, _, err := uc.Login(ctx, "alice", "password123", &code); err != nil {
		t.Fatalf("failed request consumed code: %v", err)
	}
}
