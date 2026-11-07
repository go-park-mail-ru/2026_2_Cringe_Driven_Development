// Package postgres хранит пользователей в PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/observability"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
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

func NewUserRepository(db Querier) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) CreateUser(ctx context.Context, login, passwordHash string) (domain.User, error) {
	started := time.Now()
	u := domain.User{Login: login}
	err := r.db.QueryRow(ctx,
		`INSERT INTO users (login, password_hash) VALUES ($1, $2) RETURNING id`,
		login, passwordHash,
	).Scan(&u.ID)
	observability.LogStorage(ctx, "user.create", started, err)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return domain.User{}, usecase.ErrLoginTaken
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("insert user: %w", err)
	}
	return u, nil
}

func (r *UserRepository) UserByLogin(ctx context.Context, login string) (u domain.User, passwordHash string, err error) {
	started := time.Now()
	err = r.db.QueryRow(ctx,
		`SELECT id, login, password_hash, COALESCE(avatar_key, ''), totp_enabled FROM users WHERE lower(login) = lower($1)`,
		login,
	).Scan(&u.ID, &u.Login, &passwordHash, &u.AvatarKey, &u.Has2FA)
	observability.LogStorage(ctx, "user.by_login", started, err)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, "", usecase.ErrUserNotFound
	}
	if err != nil {
		return domain.User{}, "", fmt.Errorf("select user by login: %w", err)
	}
	return u, passwordHash, nil
}

func (r *UserRepository) UserByID(ctx context.Context, id int64) (domain.User, error) {
	started := time.Now()
	u := domain.User{ID: id}
	err := r.db.QueryRow(ctx, `SELECT login, COALESCE(avatar_key, ''), totp_enabled FROM users WHERE id = $1`, id).Scan(&u.Login, &u.AvatarKey, &u.Has2FA)
	observability.LogStorage(ctx, "user.by_id", started, err)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, usecase.ErrUserNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("select user by id: %w", err)
	}
	return u, nil
}

func (r *UserRepository) CredentialsByID(ctx context.Context, id int64) (domain.User, string, error) {
	var u domain.User
	var hash string
	err := r.db.QueryRow(ctx, `SELECT id, login, password_hash, COALESCE(avatar_key, ''), totp_enabled FROM users WHERE id = $1`, id).Scan(&u.ID, &u.Login, &hash, &u.AvatarKey, &u.Has2FA)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, "", usecase.ErrUserNotFound
	}
	if err != nil {
		return domain.User{}, "", fmt.Errorf("select credentials: %w", err)
	}
	return u, hash, nil
}
func (r *UserRepository) UpdateLogin(ctx context.Context, id int64, login string) (domain.User, error) {
	var u domain.User
	err := r.db.QueryRow(ctx, `UPDATE users SET login = $2, updated_at = clock_timestamp() WHERE id = $1 RETURNING id, login, COALESCE(avatar_key, ''), totp_enabled`, id, login).Scan(&u.ID, &u.Login, &u.AvatarKey, &u.Has2FA)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return domain.User{}, usecase.ErrLoginTaken
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, usecase.ErrUserNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("update login: %w", err)
	}
	return u, nil
}
func (r *UserRepository) CompareAndSwapPassword(ctx context.Context, id int64, oldHash, newHash string) error {
	result, err := r.db.Exec(ctx, `UPDATE users SET password_hash = $3, updated_at = clock_timestamp() WHERE id = $1 AND password_hash = $2`, id, oldHash, newHash)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if result.RowsAffected() != 1 {
		return usecase.ErrInvalidCurrentPassword
	}
	return nil
}
func (r *UserRepository) SwapAvatar(ctx context.Context, id int64, newKey string) (domain.User, string, error) {
	beginner, ok := r.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return domain.User{}, "", errors.New("user repository does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.User{}, "", fmt.Errorf("begin avatar swap: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var old string
	err = tx.QueryRow(ctx, `SELECT COALESCE(avatar_key, '') FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&old)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, "", usecase.ErrUserNotFound
	}
	if err != nil {
		return domain.User{}, "", fmt.Errorf("lock avatar: %w", err)
	}
	var u domain.User
	err = tx.QueryRow(ctx, `UPDATE users SET avatar_key = $2, updated_at = clock_timestamp() WHERE id = $1 RETURNING id, login, COALESCE(avatar_key, ''), totp_enabled`, id, newKey).Scan(&u.ID, &u.Login, &u.AvatarKey, &u.Has2FA)
	if err != nil {
		return domain.User{}, "", fmt.Errorf("swap avatar: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.User{}, "", fmt.Errorf("commit avatar swap: %w", err)
	}
	return u, old, nil
}
