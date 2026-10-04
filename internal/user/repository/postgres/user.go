// Package postgres хранит пользователей и сессии в PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/models"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/repository"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier описывает то общее, что есть у *pgxpool.Pool и pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const uniqueViolation = "23505"

type UserRepository struct {
	db Querier
}

var _ repository.Repository = (*UserRepository)(nil)

func NewUserRepository(db Querier) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) CreateUser(ctx context.Context, login, passwordHash string) (models.User, error) {
	u := models.User{Login: login}
	err := r.db.QueryRow(ctx,
		`INSERT INTO users (login, password_hash) VALUES ($1, $2) RETURNING id`,
		login, passwordHash,
	).Scan(&u.ID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return models.User{}, repository.ErrLoginTaken
	}
	if err != nil {
		return models.User{}, fmt.Errorf("insert user: %w", err)
	}
	return u, nil
}

func (r *UserRepository) UserByLogin(ctx context.Context, login string) (u models.User, passwordHash string, err error) {
	err = r.db.QueryRow(ctx,
		`SELECT id, login, password_hash FROM users WHERE lower(login) = lower($1)`,
		login,
	).Scan(&u.ID, &u.Login, &passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.User{}, "", repository.ErrUserNotFound
	}
	if err != nil {
		return models.User{}, "", fmt.Errorf("select user by login: %w", err)
	}
	return u, passwordHash, nil
}

func (r *UserRepository) UserByID(ctx context.Context, id int64) (models.User, error) {
	u := models.User{ID: id}
	err := r.db.QueryRow(ctx, `SELECT login FROM users WHERE id = $1`, id).Scan(&u.Login)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.User{}, repository.ErrUserNotFound
	}
	if err != nil {
		return models.User{}, fmt.Errorf("select user by id: %w", err)
	}
	return u, nil
}

func (r *UserRepository) CreateSession(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO refresh_sessions (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, tokenHash, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("insert refresh session: %w", err)
	}
	return nil
}

// DeleteSession удаляет и читает одним запросом: из двух одновременных
// refresh с одним токеном сессию получит только один.
func (r *UserRepository) DeleteSession(ctx context.Context, tokenHash string) (userID int64, expiresAt time.Time, err error) {
	err = r.db.QueryRow(ctx,
		`DELETE FROM refresh_sessions WHERE token_hash = $1 RETURNING user_id, expires_at`,
		tokenHash,
	).Scan(&userID, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, time.Time{}, repository.ErrSessionNotFound
	}
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("delete refresh session: %w", err)
	}
	return userID, expiresAt, nil
}
