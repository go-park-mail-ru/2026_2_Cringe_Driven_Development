// Package models содержит типы, которые передаются между слоями пользователя.
package models

import "time"

type User struct {
	ID    int64
	Login string
}

// Session получает клиент после входа или refresh.
type Session struct {
	UserID       int64
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}
