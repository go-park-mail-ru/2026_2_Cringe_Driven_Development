package usecase

import "time"

// Session получает клиент после входа или refresh.
type Session struct {
	SessionClaims
	AccessExpiresAt time.Time
	AccessToken     string
	RefreshToken    string
	ExpiresAt       time.Time // Срок действия refresh-сессии.
}
