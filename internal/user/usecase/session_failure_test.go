package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/mock/gomock"
)

type faultSessions struct {
	*fakeRepo
	createErr, rotateErr error
	versionReads         int
}

func (s *faultSessions) CurrentVersion(ctx context.Context, id int64) (SessionVersion, error) {
	s.versionReads++
	return s.fakeRepo.CurrentVersion(ctx, id)
}
func (s *faultSessions) Create(ctx context.Context, n NewSession) error {
	if s.createErr != nil {
		return s.createErr
	}
	return s.fakeRepo.Create(ctx, n)
}
func (s *faultSessions) Rotate(ctx context.Context, old string, n NewSession) error {
	if s.rotateErr != nil {
		return s.rotateErr
	}
	return s.fakeRepo.Rotate(ctx, old, n)
}

func TestRegistrationSurvivesSessionFailure(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	failure := errors.New("redis down")
	sessions := &faultSessions{fakeRepo: repo, createErr: failure}
	uc := NewUserUsecase(repo, sessions, flowIssuer(t, 2), flowPasswords{}, flowRefresh{}, time.Hour)
	_, pair, err := uc.Register(ctx, "alice", "password123")
	if !errors.Is(err, failure) || pair != (Session{}) {
		t.Fatalf("pair=%v err=%v", pair, err)
	}
	if _, _, err = repo.UserByLogin(ctx, "alice"); err != nil {
		t.Fatal("created account lost")
	}
	sessions.createErr = nil
	_, pair, err = uc.Login(ctx, "alice", "password123")
	if err != nil || pair.AccessToken == "" {
		t.Fatalf("cannot login after recovery: %v", err)
	}
}
func TestVersionConflictDoesNotRetryAuthorization(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	repo.addUser("alice", "password123")
	sessions := &faultSessions{fakeRepo: repo, createErr: ErrVersionChanged}
	uc := NewUserUsecase(repo, sessions, flowIssuer(t, 1), flowPasswords{}, flowRefresh{}, time.Hour)
	_, pair, err := uc.Login(ctx, "alice", "password123")
	if !errors.Is(err, ErrInvalidCredentials) || pair != (Session{}) || sessions.versionReads != 1 {
		t.Fatalf("reads=%d pair=%v err=%v", sessions.versionReads, pair, err)
	}
}
func TestRefreshPreparationAndRejectedRotationPreserveOldSession(t *testing.T) {
	for _, tc := range []string{"signing", "rotation", "invalid session"} {
		t.Run(tc, func(t *testing.T) {
			ctx := context.Background()
			repo := newFakeRepo()
			repo.addUser("alice", "password123")
			sessions := &faultSessions{fakeRepo: repo}
			issuer := NewMockTokenIssuer(gomock.NewController(t))
			failure := errors.New("failed")
			issuer.EXPECT().IssueAccess(gomock.Any()).Return("access", time.Now().Add(time.Minute), nil)
			uc := NewUserUsecase(repo, sessions, issuer, flowPasswords{}, flowRefresh{}, time.Hour)
			_, first, err := uc.Login(ctx, "alice", "password123")
			if err != nil {
				t.Fatal(err)
			}
			want := failure
			if tc == "signing" {
				issuer.EXPECT().IssueAccess(first.SessionClaims).Return("", time.Time{}, failure)
			} else {
				issuer.EXPECT().IssueAccess(first.SessionClaims).Return("next", time.Now().Add(time.Minute), nil)
				sessions.rotateErr = failure
				if tc == "invalid session" {
					sessions.rotateErr = ErrInvalidSession
					want = ErrInvalidRefresh
				}
			}
			next, err := uc.Refresh(ctx, first.RefreshToken)
			if !errors.Is(err, want) || next != (Session{}) {
				t.Fatalf("pair=%v err=%v", next, err)
			}
			if _, err := repo.Find(ctx, (flowRefresh{}).Hash(first.RefreshToken)); err != nil {
				t.Fatal("failed preparation lost old session")
			}
		})
	}
}
func TestLoginSIDUniqueAndRefreshStable(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	repo.addUser("alice", "password123")
	uc := NewUserUsecase(repo, repo, flowIssuer(t, 3), flowPasswords{}, flowRefresh{}, time.Hour)
	_, a, err := uc.Login(ctx, "alice", "password123")
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := uc.Login(ctx, "alice", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if a.SessionID == "" || a.SessionID == b.SessionID || a.Version == 0 {
		t.Fatal("login reused sid")
	}
	next, err := uc.Refresh(ctx, a.RefreshToken)
	if err != nil || next.SessionClaims != a.SessionClaims {
		t.Fatalf("claims=%v err=%v", next.SessionClaims, err)
	}
	if next.ExpiresAt.Before(time.Now().Add(59 * time.Minute)) {
		t.Fatal("refresh not renewed for full TTL")
	}
}
