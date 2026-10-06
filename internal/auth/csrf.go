package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const CSRFCookieName = "__Host-csrf"
const CSRFHeaderName = "X-CSRF-Token"

// CSRF выпускает токены и проверяет их привязку к пользователю.
type CSRF struct {
	secret []byte
	ttl    time.Duration
}

func NewCSRF(secret []byte, ttl time.Duration) *CSRF {
	return &CSRF{secret: append([]byte(nil), secret...), ttl: ttl}
}

func (c *CSRF) Anonymous() string {
	return base64.RawURLEncoding.EncodeToString(csrfNonce())
}

func (c *CSRF) Signed(userID int64) string {
	nonce := csrfNonce()
	return base64.RawURLEncoding.EncodeToString(nonce) + "." + base64.RawURLEncoding.EncodeToString(c.signature(userID, nonce))
}

func (c *CSRF) Valid(token string, userID int64) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || userID <= 0 {
		return false
	}
	nonce, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil || len(nonce) != 32 {
		return false
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	return err == nil && len(signature) == sha256.Size && hmac.Equal(signature, c.signature(userID, nonce))
}

func (c *CSRF) Cookie(token string) *http.Cookie {
	return &http.Cookie{Name: CSRFCookieName, Value: token, Path: "/", Secure: true,
		SameSite: http.SameSiteLaxMode, MaxAge: int(c.ttl / time.Second)}
}

func (c *CSRF) signature(userID int64, nonce []byte) []byte {
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write([]byte(strconv.FormatInt(userID, 10) + "."))
	_, _ = mac.Write(nonce)
	return mac.Sum(nil)
}

func csrfNonce() []byte {
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	return nonce
}
