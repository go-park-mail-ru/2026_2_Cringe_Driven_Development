package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCreateUserMapsUniqueViolation(t *testing.T) {
	db := newPool(t)
	db.ExpectQuery(createUserSQL).WithArgs("login", "hash").WillReturnError(&pgconn.PgError{Code: "23505"})
	repo := NewUserRepository(db)
	_, err := repo.CreateUser(context.Background(), "login", "hash")
	if !errors.Is(err, usecase.ErrLoginTaken) {
		t.Fatalf("error=%v", err)
	}
}
