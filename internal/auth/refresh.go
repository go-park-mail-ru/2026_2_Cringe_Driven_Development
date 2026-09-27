package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// NewRefreshToken возвращает случайный токен для cookie и его хеш для базы.
func NewRefreshToken() (token, hash string) {
	b := make([]byte, 32)
	// С Go 1.24 rand.Read никогда не возвращает ошибку.
	_, _ = rand.Read(b)
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashRefreshToken(token)
}

// HashRefreshToken — SHA-256 токена. Токен случайный и длинный, поэтому
// в отличие от пароля ему не нужен медленный bcrypt.
func HashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
