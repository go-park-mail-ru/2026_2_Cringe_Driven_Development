package httptransport

import (
	"context"

	notebookmodels "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	notebookusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
)

// Неиспользуемые методы встроенных интерфейсов не должны вызываться в этих тестах.
type bffUsers struct {
	usecase.Usecase
	session   usecase.Session
	mutations int
}

func (u *bffUsers) Login(context.Context, string, string) (domain.User, usecase.Session, error) {
	return domain.User{ID: 1, Login: "bob"}, u.session, nil
}
func (u *bffUsers) Register(ctx context.Context, login, password string) (domain.User, usecase.Session, error) {
	return u.Login(ctx, login, password)
}
func (u *bffUsers) CurrentUser(context.Context, int64) (domain.User, error) {
	return domain.User{ID: 1, Login: "bob"}, nil
}
func (u *bffUsers) validateRefresh(refresh string) error {
	if refresh != u.session.RefreshToken || refresh == "" {
		return usecase.ErrInvalidRefresh
	}
	return nil
}
func (u *bffUsers) Refresh(_ context.Context, refresh string) (usecase.Session, error) {
	if err := u.validateRefresh(refresh); err != nil {
		return usecase.Session{}, err
	}
	u.mutations++
	u.session.RefreshToken += "-rotated"
	return u.session, nil
}
func (u *bffUsers) Logout(_ context.Context, refresh string, _ usecase.SessionClaims) error {
	if err := u.validateRefresh(refresh); err != nil {
		return err
	}
	u.mutations++
	u.session.RefreshToken = ""
	return nil
}

type bffNotebooks struct{ notebookusecase.Usecase }

func (bffNotebooks) Create(_ context.Context, ownerID int64, name string) (notebookmodels.Notebook, error) {
	if ownerID != 1 {
		panic("user context lost")
	}
	return notebookmodels.Notebook{ID: 1, Name: name}, nil
}
func (bffNotebooks) CreateCell(_ context.Context, _, ownerID int64, _ string, kind notebookmodels.CellKind, _ *int) (notebookmodels.Cell, error) {
	if ownerID != 1 {
		panic("user context lost")
	}
	return notebookmodels.Cell{ID: "cell", Kind: kind}, nil
}
func (bffNotebooks) DeleteCell(_ context.Context, _, ownerID int64, _ string, _ int) error {
	if ownerID != 1 {
		panic("user context lost")
	}
	return nil
}
