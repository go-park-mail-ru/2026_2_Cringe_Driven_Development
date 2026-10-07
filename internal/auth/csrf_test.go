package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestCSRFTokens(t *testing.T) {
	csrf := NewCSRF([]byte("csrf-secret"), 30*24*time.Hour)
	anonymous := csrf.Anonymous()
	nonce, err := base64.RawURLEncoding.DecodeString(anonymous)
	if err != nil || len(nonce) != 32 || strings.Contains(anonymous, ".") {
		t.Fatalf("invalid anonymous token: %q", anonymous)
	}
	signed := csrf.Signed(42)
	parts := strings.Split(signed, ".")
	nonce, err = base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(nonce) != 32 {
		t.Fatal("invalid signed nonce")
	}
	mac := hmac.New(sha256.New, []byte("csrf-secret"))
	_, _ = mac.Write(append([]byte("42."), nonce...))
	if parts[1] != base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) {
		t.Fatal("signature does not match wire format")
	}
	if !csrf.Valid(signed, 42) {
		t.Fatal("valid signature rejected")
	}
	for _, token := range []string{anonymous, "", signed + ".extra", parts[0] + ".", "!." + parts[1], parts[0] + "." + base64.RawURLEncoding.EncodeToString(make([]byte, 32)), base64.RawURLEncoding.EncodeToString(make([]byte, 31)) + "." + parts[1]} {
		if csrf.Valid(token, 42) {
			t.Errorf("invalid token accepted: %q", token)
		}
	}
	if csrf.Valid(signed, 43) || NewCSRF([]byte("other-secret"), time.Hour).Valid(signed, 42) {
		t.Fatal("signature accepted for another user or secret")
	}
	if csrf.Anonymous() == anonymous || csrf.Signed(42) == signed {
		t.Fatal("token was not rotated")
	}
	cookie := csrf.Cookie(signed)
	if cookie.Name != CSRFCookieName || cookie.Path != "/" || !cookie.Secure || cookie.HttpOnly || cookie.Domain != "" || cookie.MaxAge != 30*24*3600 {
		t.Fatalf("invalid cookie: %+v", cookie)
	}
}
