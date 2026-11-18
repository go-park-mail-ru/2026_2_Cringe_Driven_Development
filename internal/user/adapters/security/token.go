package security

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"github.com/golang-jwt/jwt/v5"
)

var ErrInvalidToken = errors.New("invalid access token")

// AccessToken выпускает и проверяет JWT для учётных данных сессии.
type AccessToken struct {
	secret []byte
	ttl    time.Duration
}

// accessClaims описывает зарегистрированные поля JWT и ссылку на сессию.
type accessClaims struct {
	jwt.RegisteredClaims
	SessionID string `json:"sid"`
	Version   string `json:"session_version"`
}

// NewAccessToken связывает секрет подписи JWT и срок жизни access-токена.
func NewAccessToken(secret []byte, ttl time.Duration) *AccessToken {
	return &AccessToken{secret: secret, ttl: ttl}
}

// Issue выпускает JWT для указанного пользователя.
func (t *AccessToken) Issue(claims usecase.SessionClaims) (string, error) {
	token, _, err := t.IssueAccess(claims)
	return token, err
}

// IssueAccess выпускает JWT с идентификатором и версией сессии.
func (t *AccessToken) IssueAccess(c usecase.SessionClaims) (string, time.Time, error) {
	if c.UserID <= 0 || strings.TrimSpace(c.SessionID) == "" || c.Version == 0 {
		return "", time.Time{}, ErrInvalidToken
	}
	claims := accessClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: strconv.FormatInt(c.UserID, 10), ExpiresAt: jwt.NewNumericDate(time.Now().Add(t.ttl))}, SessionID: c.SessionID, Version: strconv.FormatUint(uint64(c.Version), 10)}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return token, claims.ExpiresAt.Time, nil
}

// Parse проверяет подпись, срок жизни и обязательные данные JWT.
func (t *AccessToken) Parse(token string) (usecase.SessionClaims, error) {
	var c accessClaims
	_, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) { return t.secret, nil }, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
	if err != nil {
		return usecase.SessionClaims{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	id, idErr := strconv.ParseInt(c.Subject, 10, 64)
	version, versionErr := strconv.ParseUint(c.Version, 10, 64)
	if idErr != nil || id <= 0 || strconv.FormatInt(id, 10) != c.Subject || strings.TrimSpace(c.SessionID) == "" || versionErr != nil || version == 0 || strconv.FormatUint(version, 10) != c.Version {
		return usecase.SessionClaims{}, ErrInvalidToken
	}
	return usecase.SessionClaims{UserID: id, SessionID: c.SessionID, Version: usecase.SessionVersion(version)}, nil
}
