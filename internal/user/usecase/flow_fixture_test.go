package usecase

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"github.com/google/uuid"
	"go.uber.org/mock/gomock"
	"golang.org/x/crypto/bcrypt"
)

// fakeRepo stores users and sessions for token rotation/logout and logging flows.
// This fixture does not verify PostgreSQL behavior.
type fakeRepo struct {
	mu       sync.Mutex
	users    map[int64]fakeUser
	sessions map[string]RefreshSession
	// lastID растёт с каждым пользователем, как IDENTITY в Postgres.
	lastID int64
}

type fakeUser struct {
	domain.User
	hash string
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{users: map[int64]fakeUser{}, sessions: map[string]RefreshSession{}}
}

// addUser кладёт пользователя с паролем. MinCost, чтобы тесты не ждали bcrypt.
func (r *fakeRepo) addUser(login, password string) domain.User {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	r.lastID++
	u := domain.User{ID: r.lastID, Login: login}
	r.users[u.ID] = fakeUser{User: u, hash: string(hash)}
	return u
}

func (r *fakeRepo) CreateUser(_ context.Context, login, passwordHash string) (domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range r.users {
		if strings.EqualFold(u.Login, login) {
			return domain.User{}, ErrLoginTaken
		}
	}
	r.lastID++
	u := domain.User{ID: r.lastID, Login: login}
	r.users[u.ID] = fakeUser{User: u, hash: passwordHash}
	return u, nil
}

func (r *fakeRepo) UserByLogin(_ context.Context, login string) (domain.User, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range r.users {
		if strings.EqualFold(u.Login, login) {
			return u.User, u.hash, nil
		}
	}
	return domain.User{}, "", ErrUserNotFound
}

func (r *fakeRepo) UserByID(_ context.Context, id int64) (domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	if !ok {
		return domain.User{}, ErrUserNotFound
	}
	return u.User, nil
}

func (r *fakeRepo) CurrentVersion(context.Context, int64) (SessionVersion, error) { return 1, nil }
func (r *fakeRepo) Create(_ context.Context, s NewSession) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[s.TokenHash] = s.RefreshSession
	return nil
}
func (r *fakeRepo) Find(_ context.Context, h string) (RefreshSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[h]
	if !ok {
		return RefreshSession{}, ErrInvalidSession
	}
	return s, nil
}
func (r *fakeRepo) Rotate(_ context.Context, h string, n NewSession) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[h]
	if !ok || s.SessionClaims != n.SessionClaims {
		return ErrInvalidSession
	}
	delete(r.sessions, h)
	r.sessions[n.TokenHash] = n.RefreshSession
	return nil
}
func (r *fakeRepo) Revoke(_ context.Context, h string, c SessionClaims) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[h]
	if !ok || s.SessionClaims != c {
		return ErrInvalidSession
	}
	delete(r.sessions, h)
	return nil
}
func (r *fakeRepo) RevokeAll(context.Context, int64) error { panic("unused in these flows") }
func (r *fakeRepo) ValidateAccess(context.Context, SessionClaims) error {
	panic("unused in these flows")
}

type flowPasswords struct{}

func (flowPasswords) Hash(p string) (string, error) {
	h, e := bcrypt.GenerateFromPassword([]byte(p), bcrypt.MinCost)
	return string(h), e
}
func (flowPasswords) Compare(h, p string) (bool, error) {
	return bcrypt.CompareHashAndPassword([]byte(h), []byte(p)) == nil, nil
}

type flowRefresh struct{}

func (flowRefresh) New() (string, string) { v := uuid.NewString(); return v, (flowRefresh{}).Hash(v) }
func (flowRefresh) Hash(v string) string  { return fmt.Sprintf("%x", sha256.Sum256([]byte(v))) }

func flowIssuer(t *testing.T, calls int) *MockTokenIssuer {
	t.Helper()
	issuer := NewMockTokenIssuer(gomock.NewController(t))
	issuer.EXPECT().IssueAccess(gomock.Any()).Return("access-1", time.Now().Add(time.Minute), nil).Times(calls)
	return issuer
}

func (r *fakeRepo) CredentialsByID(ctx context.Context, id int64) (domain.User, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	if !ok {
		return domain.User{}, "", ErrUserNotFound
	}
	return u.User, u.hash, nil
}
func (r *fakeRepo) UpdateLogin(ctx context.Context, id int64, login string) (domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	if !ok {
		return domain.User{}, ErrUserNotFound
	}
	for otherID, other := range r.users {
		if otherID != id && strings.EqualFold(other.Login, login) {
			return domain.User{}, ErrLoginTaken
		}
	}
	u.Login = login
	r.users[id] = u
	return u.User, nil
}
func (r *fakeRepo) CompareAndSwapPassword(ctx context.Context, id int64, oldHash, newHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	if !ok {
		return ErrUserNotFound
	}
	if u.hash != oldHash {
		return ErrInvalidCurrentPassword
	}
	u.hash = newHash
	r.users[id] = u
	return nil
}
func (r *fakeRepo) SwapAvatar(ctx context.Context, id int64, key string) (domain.User, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	if !ok {
		return domain.User{}, "", ErrUserNotFound
	}
	old := u.AvatarKey
	u.AvatarKey = key
	r.users[id] = u
	return u.User, old, nil
}
