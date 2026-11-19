// Package domain содержит типы, которые передаются между слоями пользователя.
package domain

// User содержит идентификатор, логин и данные профиля пользователя.
type User struct {
	Has2FA    bool
	ID        int64
	Login     string
	AvatarKey string
}

// UserSuggestion contains only public identity fields.
type UserSuggestion struct {
	ID    int64
	Login string
}
