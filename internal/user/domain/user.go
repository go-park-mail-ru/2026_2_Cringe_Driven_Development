// Package models содержит типы, которые передаются между слоями пользователя.
package domain

type User struct {
	ID        int64
	Login     string
	AvatarKey string
}
