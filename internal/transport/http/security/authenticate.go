package security

import "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"

// TokenParser проверяет токен и возвращает данные сессии для авторизации.
type TokenParser interface {
	Parse(token string) (usecase.SessionClaims, error)
}
