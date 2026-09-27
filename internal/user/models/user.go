// Package models содержит типы, которые передаются между слоями пользователя.
package models

import (
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID    uuid.UUID
	Login string
}

// Session получает клиент после входа или refresh.
type Session struct {
	UserID       uuid.UUID
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}
