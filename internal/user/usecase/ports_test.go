package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"go.uber.org/mock/gomock"
)

func TestPasswordHashFailureDoesNotCreateUser(t *testing.T) {
	f := newUserFixture(t)
	failure := errors.New("hash failed")
	f.passwords.EXPECT().Hash("password123").Return("", failure)
	_, _, err := f.uc.Register(context.Background(), "alice", "password123")
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
func TestTokenIssueFailureDoesNotCreateSession(t *testing.T) {
	f := newUserFixture(t)
	ctx := context.Background()
	failure := errors.New("sign failed")
	f.users.EXPECT().UserByLogin(ctx, "alice").Return(domain.User{ID: 7}, "hash", nil)
	f.users.EXPECT().CredentialsByID(ctx, int64(7)).Return(domain.User{ID: 7}, "hash", nil)
	f.sessions.EXPECT().CurrentVersion(ctx, int64(7)).Return(SessionVersion(1), nil)
	f.passwords.EXPECT().Compare("hash", "password123").Return(true, nil)
	f.tokens.EXPECT().IssueAccess(gomock.Any()).Return("", time.Time{}, failure)
	_, _, err := f.uc.Login(ctx, "alice", "password123")
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
func TestPasswordCompareFailureDoesNotCreateSession(t *testing.T) {
	f := newUserFixture(t)
	ctx := context.Background()
	failure := errors.New("verification failed")
	f.users.EXPECT().UserByLogin(ctx, "alice").Return(domain.User{ID: 7}, "hash", nil)
	f.users.EXPECT().CredentialsByID(ctx, int64(7)).Return(domain.User{ID: 7}, "hash", nil)
	f.sessions.EXPECT().CurrentVersion(ctx, int64(7)).Return(SessionVersion(1), nil)
	f.passwords.EXPECT().Compare("hash", "password123").Return(false, failure)
	_, sess, err := f.uc.Login(ctx, "alice", "password123")
	if !errors.Is(err, failure) || errors.Is(err, ErrInvalidCredentials) || sess != (Session{}) {
		t.Fatalf("session=%v err=%v", sess, err)
	}
}
func TestPasswordMismatchDoesNotCreateSession(t *testing.T) {
	f := newUserFixture(t)
	ctx := context.Background()
	f.users.EXPECT().UserByLogin(ctx, "alice").Return(domain.User{ID: 7}, "hash", nil)
	f.users.EXPECT().CredentialsByID(ctx, int64(7)).Return(domain.User{ID: 7}, "hash", nil)
	f.sessions.EXPECT().CurrentVersion(ctx, int64(7)).Return(SessionVersion(1), nil)
	f.passwords.EXPECT().Compare("hash", "wrong-password").Return(false, nil)
	_, sess, err := f.uc.Login(ctx, "alice", "wrong-password")
	if !errors.Is(err, ErrInvalidCredentials) || sess != (Session{}) {
		t.Fatalf("session=%v err=%v", sess, err)
	}
}
func TestSessionCreateFailure(t *testing.T) {
	f := newUserFixture(t)
	ctx := context.Background()
	failure := errors.New("insert failed")
	f.tokens.EXPECT().IssueAccess(gomock.Any()).Return("access", f.now.Add(time.Minute), nil)
	f.refresh.EXPECT().New().Return("refresh", "hash")
	f.sessions.EXPECT().Create(ctx, gomock.Any()).Return(failure)
	sess, err := f.uc.startSession(ctx, 7, 1)
	if !errors.Is(err, failure) || sess != (Session{}) {
		t.Fatalf("session=%v err=%v", sess, err)
	}
}
