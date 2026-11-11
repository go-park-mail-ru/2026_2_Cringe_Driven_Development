package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"go.uber.org/mock/gomock"
)

// Фиксируем отзыв непосредственно перед чтением текущих учётных данных.
type passwordChangeOnVersion struct {
	*faultSessions
	change func()
}

func (s *passwordChangeOnVersion) CurrentVersion(ctx context.Context, id int64) (SessionVersion, error) {
	v, err := s.faultSessions.CurrentVersion(ctx, id)
	s.change()
	return v, err
}

func TestLoginRechecksCredentialsAfterVersionRead(t *testing.T) {
	repo := newFakeRepo()
	u := repo.addUser("alice", "password123")
	hash, _ := (flowPasswords{}).Hash("newpassword")
	sessions := &passwordChangeOnVersion{faultSessions: &faultSessions{fakeRepo: repo}, change: func() {
		repo.mu.Lock()
		user := repo.users[u.ID]
		user.hash = hash
		repo.users[u.ID] = user
		repo.mu.Unlock()
	}}
	uc := NewUserUsecase(repo, sessions, flowIssuer(t, 0), flowPasswords{}, flowRefresh{}, refreshTTL)
	_, pair, err := uc.Login(context.Background(), "alice", "password123")
	if !errors.Is(err, ErrInvalidCredentials) || pair != (Session{}) {
		t.Fatalf("pair=%v err=%v", pair, err)
	}
}
func TestRegistrationRechecksCredentialsBeforeIssuance(t *testing.T) {
	repo := newFakeRepo()
	hash, _ := (flowPasswords{}).Hash("newpassword")
	sessions := &passwordChangeOnVersion{faultSessions: &faultSessions{fakeRepo: repo}, change: func() {
		repo.mu.Lock()
		user := repo.users[1]
		user.hash = hash
		repo.users[1] = user
		repo.mu.Unlock()
	}}
	uc := NewUserUsecase(repo, sessions, flowIssuer(t, 0), flowPasswords{}, flowRefresh{}, refreshTTL)
	_, pair, err := uc.Register(context.Background(), "alice", "password123")
	if err == nil || pair != (Session{}) {
		t.Fatalf("pair=%v err=%v", pair, err)
	}
	if _, _, err = repo.UserByLogin(context.Background(), "alice"); err != nil {
		t.Fatal("registration lost persisted account")
	}
}

func TestCapturedVersionConflictRejectsRegistrationWithoutRemovingAccount(t *testing.T) {
	repo := newFakeRepo()
	sessions := &faultSessions{fakeRepo: repo, createErr: ErrVersionChanged}
	uc := NewUserUsecase(repo, sessions, flowIssuer(t, 1), flowPasswords{}, flowRefresh{}, refreshTTL)
	_, pair, err := uc.Register(context.Background(), "alice", "password123")
	if !errors.Is(err, ErrVersionChanged) || pair != (Session{}) || sessions.versionReads != 1 {
		t.Fatalf("pair=%v err=%v reads=%d", pair, err, sessions.versionReads)
	}
	if _, _, err = repo.UserByLogin(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
}

func TestLoginReadsCurrentPasswordInsteadOfDiscoveryHash(t *testing.T) {
	f := newUserFixture(t)
	ctx := context.Background()
	u := domain.User{ID: 7, Login: "alice"}
	gomock.InOrder(
		f.users.EXPECT().UserByLogin(ctx, "alice").Return(u, "obsolete", nil),
		f.sessions.EXPECT().CurrentVersion(ctx, int64(7)).Return(SessionVersion(8), nil),
		f.users.EXPECT().CredentialsByID(ctx, int64(7)).Return(u, "current", nil),
		f.passwords.EXPECT().Compare("current", "newpassword").Return(true, nil),
		f.tokens.EXPECT().IssueAccess(gomock.Any()).Return("access", f.now, nil),
		f.refresh.EXPECT().New().Return("refresh", "hash"),
		f.sessions.EXPECT().Create(ctx, gomock.Any()).DoAndReturn(func(_ context.Context, n NewSession) error {
			if n.Version != 8 {
				t.Fatalf("version=%d", n.Version)
			}
			return nil
		}),
	)
	_, _, err := f.uc.Login(ctx, "alice", "newpassword")
	if err != nil {
		t.Fatal(err)
	}
}

func TestLoginSessionVersionFailureDoesNotCheckPassword(t *testing.T) {
	f := newUserFixture(t)
	ctx := context.Background()
	failure := errors.New("redis unavailable")
	f.users.EXPECT().UserByLogin(ctx, "alice").Return(domain.User{ID: 7}, "hash", nil)
	f.sessions.EXPECT().CurrentVersion(ctx, int64(7)).Return(SessionVersion(0), failure)
	_, pair, err := f.uc.Login(ctx, "alice", "password123")
	if !errors.Is(err, failure) || pair != (Session{}) {
		t.Fatalf("pair=%v err=%v", pair, err)
	}
}
