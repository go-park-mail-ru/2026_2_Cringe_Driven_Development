package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v5"
)

func TestPasswordCAS(t *testing.T) {
	for _, rows := range []int64{0, 1} {
		db := newPool(t)
		db.ExpectExec(`UPDATE users SET password_hash = $3, updated_at = clock_timestamp() WHERE id = $1 AND password_hash = $2`).WithArgs(int64(7), "old", "new").WillReturnResult(pgxmock.NewResult("UPDATE", rows))
		err := NewUserRepository(db).CompareAndSwapPassword(context.Background(), 7, "old", "new")
		if rows == 0 && !errors.Is(err, usecase.ErrInvalidCurrentPassword) || rows == 1 && err != nil {
			t.Fatalf("rows=%d err=%v", rows, err)
		}
	}
}
func TestUpdateLoginConflict(t *testing.T) {
	db := newPool(t)
	db.ExpectQuery(`UPDATE users SET login = $2, updated_at = clock_timestamp() WHERE id = $1 RETURNING id, login, COALESCE(avatar_key, ''), totp_enabled`).WithArgs(int64(7), "ALICE").WillReturnError(&pgconn.PgError{Code: "23505"})
	_, err := NewUserRepository(db).UpdateLogin(context.Background(), 7, "ALICE")
	if !errors.Is(err, usecase.ErrLoginTaken) {
		t.Fatal(err)
	}
}
func TestSwapAvatarTransaction(t *testing.T) {
	db := newPool(t)
	db.ExpectBegin()
	db.ExpectQuery(`SELECT COALESCE(avatar_key, '') FROM users WHERE id = $1 FOR UPDATE`).WithArgs(int64(7)).WillReturnRows(pgxmock.NewRows([]string{"avatar_key"}).AddRow("old"))
	db.ExpectQuery(`UPDATE users SET avatar_key = $2, updated_at = clock_timestamp() WHERE id = $1 RETURNING id, login, COALESCE(avatar_key, ''), totp_enabled`).WithArgs(int64(7), "new").WillReturnRows(pgxmock.NewRows([]string{"id", "login", "avatar_key", "totp_enabled"}).AddRow(int64(7), "alice", "new", false))
	db.ExpectCommit()
	u, old, err := NewUserRepository(db).SwapAvatar(context.Background(), 7, "new")
	if err != nil || old != "old" || u.AvatarKey != "new" {
		t.Fatalf("user=%v old=%s err=%v", u, old, err)
	}
}

func TestSwapAvatarUpdateFailureRollsBack(t *testing.T) {
	db := newPool(t)
	failure := errors.New("write unavailable")
	db.ExpectBegin()
	db.ExpectQuery(`SELECT COALESCE(avatar_key, '') FROM users WHERE id = $1 FOR UPDATE`).WithArgs(int64(7)).WillReturnRows(pgxmock.NewRows([]string{"avatar_key"}).AddRow("old"))
	db.ExpectQuery(`UPDATE users SET avatar_key = $2, updated_at = clock_timestamp() WHERE id = $1 RETURNING id, login, COALESCE(avatar_key, ''), totp_enabled`).WithArgs(int64(7), "new").WillReturnError(failure)
	db.ExpectRollback()
	u, old, err := NewUserRepository(db).SwapAvatar(context.Background(), 7, "new")
	if !errors.Is(err, failure) || u.ID != 0 || old != "" {
		t.Fatalf("user=%v old=%q err=%v", u, old, err)
	}
}

func TestCredentialsByIDCarriesAvatar(t *testing.T) {
	db := newPool(t)
	db.ExpectQuery(`SELECT id, login, password_hash, COALESCE(avatar_key, ''), totp_enabled FROM users WHERE id = $1`).WithArgs(int64(7)).WillReturnRows(pgxmock.NewRows([]string{"id", "login", "password_hash", "avatar_key", "totp_enabled"}).AddRow(int64(7), "alice", "hash", "avatar", false))
	u, hash, err := NewUserRepository(db).CredentialsByID(context.Background(), 7)
	if err != nil || u.AvatarKey != "avatar" || hash != "hash" {
		t.Fatalf("user=%v hash=%s err=%v", u, hash, err)
	}
}
