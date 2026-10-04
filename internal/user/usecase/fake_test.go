package usecase

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/models"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/repository"
	"golang.org/x/crypto/bcrypt"
)

// fakeRepo реализует repository.Repository в памяти вместо базы.
type fakeRepo struct {
	mu       sync.Mutex
	users    map[int64]fakeUser
	sessions map[string]fakeSession
	// lastID растёт с каждым пользователем, как IDENTITY в Postgres.
	lastID int64
}

var _ repository.Repository = (*fakeRepo)(nil)

type fakeUser struct {
	models.User
	hash string
}

type fakeSession struct {
	userID    int64
	expiresAt time.Time
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{users: map[int64]fakeUser{}, sessions: map[string]fakeSession{}}
}

// addUser кладёт пользователя с паролем. MinCost, чтобы тесты не ждали bcrypt.
func (r *fakeRepo) addUser(login, password string) models.User {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	r.lastID++
	u := models.User{ID: r.lastID, Login: login}
	r.users[u.ID] = fakeUser{User: u, hash: string(hash)}
	return u
}

func (r *fakeRepo) CreateUser(_ context.Context, login, passwordHash string) (models.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range r.users {
		if strings.EqualFold(u.Login, login) {
			return models.User{}, repository.ErrLoginTaken
		}
	}
	r.lastID++
	u := models.User{ID: r.lastID, Login: login}
	r.users[u.ID] = fakeUser{User: u, hash: passwordHash}
	return u, nil
}

func (r *fakeRepo) UserByLogin(_ context.Context, login string) (models.User, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range r.users {
		if strings.EqualFold(u.Login, login) {
			return u.User, u.hash, nil
		}
	}
	return models.User{}, "", repository.ErrUserNotFound
}

func (r *fakeRepo) UserByID(_ context.Context, id int64) (models.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	if !ok {
		return models.User{}, repository.ErrUserNotFound
	}
	return u.User, nil
}

func (r *fakeRepo) CreateSession(_ context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[tokenHash] = fakeSession{userID: userID, expiresAt: expiresAt}
	return nil
}

func (r *fakeRepo) DeleteSession(_ context.Context, tokenHash string) (int64, time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[tokenHash]
	if !ok {
		return 0, time.Time{}, repository.ErrSessionNotFound
	}
	delete(r.sessions, tokenHash)
	return s.userID, s.expiresAt, nil
}

// fakeTokens выдаёт предсказуемый access-токен.
type fakeTokens struct{}

func (fakeTokens) Issue(userID int64) (string, error) {
	return "access-" + strconv.FormatInt(userID, 10), nil
}
