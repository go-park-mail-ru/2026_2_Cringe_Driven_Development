package user

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier — то общее, что есть у *pgxpool.Pool и pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const uniqueViolation = "23505"

type Repository struct {
	db Querier
}

func NewRepository(db Querier) *Repository {
	return &Repository{db: db}
}

func (r *Repository) CreateUser(ctx context.Context, login, passwordHash string) (User, error) {
	u := User{Login: login}
	err := r.db.QueryRow(ctx,
		`INSERT INTO users (login, password_hash) VALUES ($1, $2) RETURNING id`,
		login, passwordHash,
	).Scan(&u.ID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return User{}, ErrLoginTaken
	}
	if err != nil {
		return User{}, fmt.Errorf("insert user: %w", err)
	}
	return u, nil
}

// UserByLogin ищет без учёта регистра, как и уникальный индекс.
func (r *Repository) UserByLogin(ctx context.Context, login string) (u User, passwordHash string, err error) {
	err = r.db.QueryRow(ctx,
		`SELECT id, login, password_hash FROM users WHERE lower(login) = lower($1)`,
		login,
	).Scan(&u.ID, &u.Login, &passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", ErrUserNotFound
	}
	if err != nil {
		return User{}, "", fmt.Errorf("select user by login: %w", err)
	}
	return u, passwordHash, nil
}

func (r *Repository) UserByID(ctx context.Context, id uuid.UUID) (User, error) {
	u := User{ID: id}
	err := r.db.QueryRow(ctx, `SELECT login FROM users WHERE id = $1`, id).Scan(&u.Login)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("select user by id: %w", err)
	}
	return u, nil
}

func (r *Repository) CreateSession(ctx context.Context, userID uuid.UUID, tokenHash string, expiresAt time.Time) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO refresh_sessions (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, tokenHash, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("insert refresh session: %w", err)
	}
	return nil
}

// DeleteSession удаляет сессию и возвращает, чья она и до когда жила.
// Удаление и чтение одним запросом: из двух одновременных refresh
// с одним токеном сессию получит только один.
func (r *Repository) DeleteSession(ctx context.Context, tokenHash string) (userID uuid.UUID, expiresAt time.Time, err error) {
	err = r.db.QueryRow(ctx,
		`DELETE FROM refresh_sessions WHERE token_hash = $1 RETURNING user_id, expires_at`,
		tokenHash,
	).Scan(&userID, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, time.Time{}, ErrSessionNotFound
	}
	if err != nil {
		return uuid.Nil, time.Time{}, fmt.Errorf("delete refresh session: %w", err)
	}
	return userID, expiresAt, nil
}
