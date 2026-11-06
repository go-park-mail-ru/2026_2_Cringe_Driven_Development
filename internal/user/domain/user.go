// Package models содержит типы, которые передаются между слоями пользователя.
package domain

type User struct {
	Has2FA    bool
	ID        int64
	Login     string
	AvatarKey string
}
