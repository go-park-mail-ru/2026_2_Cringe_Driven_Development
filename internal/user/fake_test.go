package user

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// fakeRepo — репозиторий в памяти вместо базы.
type fakeRepo struct {
	mu       sync.Mutex
	users    map[uuid.UUID]fakeUser
	sessions map[string]fakeSession
}

type fakeUser struct {
	User
	hash string
}

type fakeSession struct {
	userID    uuid.UUID
	expiresAt time.Time
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{users: map[uuid.UUID]fakeUser{}, sessions: map[string]fakeSession{}}
}

// addUser кладёт пользователя с паролем. MinCost — чтобы тесты не ждали bcrypt.
func (r *fakeRepo) addUser(login, password string) User {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	u := User{ID: uuid.New(), Login: login}
	r.users[u.ID] = fakeUser{User: u, hash: string(hash)}
	return u
}

func (r *fakeRepo) CreateUser(_ context.Context, login, passwordHash string) (User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range r.users {
		if strings.EqualFold(u.Login, login) {
			return User{}, ErrLoginTaken
		}
	}
	u := User{ID: uuid.New(), Login: login}
	r.users[u.ID] = fakeUser{User: u, hash: passwordHash}
	return u, nil
}

func (r *fakeRepo) UserByLogin(_ context.Context, login string) (User, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range r.users {
		if strings.EqualFold(u.Login, login) {
			return u.User, u.hash, nil
		}
	}
	return User{}, "", ErrUserNotFound
}

func (r *fakeRepo) UserByID(_ context.Context, id uuid.UUID) (User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return u.User, nil
}

func (r *fakeRepo) CreateSession(_ context.Context, userID uuid.UUID, tokenHash string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[tokenHash] = fakeSession{userID: userID, expiresAt: expiresAt}
	return nil
}

func (r *fakeRepo) DeleteSession(_ context.Context, tokenHash string) (uuid.UUID, time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[tokenHash]
	if !ok {
		return uuid.Nil, time.Time{}, ErrSessionNotFound
	}
	delete(r.sessions, tokenHash)
	return s.userID, s.expiresAt, nil
}

// fakeTokens выдаёт предсказуемый access-токен.
type fakeTokens struct{}

func (fakeTokens) Issue(userID uuid.UUID) (string, error) {
	return "access-" + userID.String(), nil
}
