package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"github.com/jackc/pgx/v5"
)

// WithAuthLock serializes setup, confirmation, login and disable for one account.
// The callback must not return tokens to the caller before this transaction commits.
func (r *UserRepository) WithAuthLock(ctx context.Context, id int64, fn func(*usecase.AuthState) error) error {
	beginner, ok := r.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return errors.New("user repository does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin authentication: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var state usecase.AuthState
	err = tx.QueryRow(ctx, `SELECT id, login, password_hash, COALESCE(avatar_key, ''), totp_enabled, totp_secret, totp_last_step FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&state.User.ID, &state.User.Login, &state.PasswordHash, &state.User.AvatarKey, &state.User.Has2FA, &state.Secret, &state.LastStep)
	if errors.Is(err, pgx.ErrNoRows) {
		return usecase.ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("lock authentication: %w", err)
	}
	old := state
	old.Secret = bytes.Clone(state.Secret)
	if err = fn(&state); err != nil {
		return err
	}
	if state.User.Has2FA != old.User.Has2FA || state.LastStep != old.LastStep || !bytes.Equal(state.Secret, old.Secret) {
		_, err = tx.Exec(ctx, `UPDATE users SET totp_enabled = $2, totp_secret = $3, totp_last_step = $4, updated_at = clock_timestamp() WHERE id = $1`, id, state.User.Has2FA, state.Secret, state.LastStep)
		if err != nil {
			return fmt.Errorf("save authentication: %w", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit authentication: %w", err)
	}
	return nil
}
