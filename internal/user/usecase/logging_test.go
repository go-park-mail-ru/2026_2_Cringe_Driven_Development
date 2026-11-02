package usecase

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/observability"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
)

func TestSuccessfulUserActionsLogOnceWithoutSecrets(t *testing.T) {
	var out bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(observability.NewHandler(slog.NewJSONHandler(&out, nil))))
	t.Cleanup(func() { slog.SetDefault(old) })
	ctx := observability.WithRequestID(context.Background(), "req-user")
	repo := newFakeRepo()
	uc := NewUserUsecase(repo, repo, flowIssuer(t, 3), flowPasswords{}, flowRefresh{}, refreshTTL)
	_, registered, err := uc.Register(ctx, "alice-SECRET", "password-SECRET")
	if err != nil {
		t.Fatal(err)
	}
	_, logged, err := uc.Login(ctx, "alice-SECRET", "password-SECRET")
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := uc.Refresh(ctx, logged.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := uc.Logout(ctx, refreshed.RefreshToken, refreshed.SessionClaims); err != nil {
		t.Fatal(err)
	}
	_ = registered
	log := out.String()
	for _, event := range []string{"user.register", "user.login", "session.refresh", "session.logout"} {
		if strings.Count(log, `"msg":"`+event+`"`) != 1 {
			t.Fatalf("event %s: %s", event, log)
		}
	}
	if strings.Count(log, `"request_id":"req-user"`) != 4 || strings.Contains(log, "SECRET") || strings.Contains(log, logged.RefreshToken) {
		t.Fatalf("unsafe logs: %s", log)
	}
}

func TestPasswordCompareFailureHasSafeCode(t *testing.T) {
	f := newUserFixture(t)
	source := errors.New("SECRET-password-hash")
	f.users.EXPECT().UserByLogin(context.Background(), "alice").Return(domain.User{ID: 7}, "hash", nil)
	f.sessions.EXPECT().CurrentVersion(context.Background(), int64(7)).Return(SessionVersion(1), nil)
	f.users.EXPECT().CredentialsByID(context.Background(), int64(7)).Return(domain.User{ID: 7}, "hash", nil)
	f.passwords.EXPECT().Compare("hash", "password").Return(false, source)
	_, _, err := f.uc.Login(context.Background(), "alice", "password")
	if !errors.Is(err, source) || observability.ErrorCode(err) != "password_compare_failed" {
		t.Fatalf("code=%q err=%v", observability.ErrorCode(err), err)
	}
}
