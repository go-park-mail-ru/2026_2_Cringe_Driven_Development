// Package security реализует криптографические зависимости сценариев пользователя.
package security

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// PasswordHasher хранит пароли с текущей стоимостью bcrypt.
type PasswordHasher struct{}

// Hash вычисляет bcrypt-хеш пароля перед сохранением.
func (PasswordHasher) Hash(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

// Compare сравнивает пароль с сохранённым bcrypt-хешем.
func (PasswordHasher) Compare(hash, password string) (bool, error) {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
