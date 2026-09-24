package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestAccessTokens(t *testing.T) {
	userID := uuid.New()
	tokens := NewAccessTokens([]byte("secret"), time.Minute)

	sign := func(method jwt.SigningMethod, key any, claims jwt.RegisteredClaims) string {
		t.Helper()
		s, err := jwt.NewWithClaims(method, claims).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	valid, err := tokens.Issue(userID)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := NewAccessTokens([]byte("secret"), -time.Minute).Issue(userID)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := NewAccessTokens([]byte("other secret"), time.Minute).Issue(userID)
	if err != nil {
		t.Fatal(err)
	}
	inAnHour := jwt.NewNumericDate(time.Now().Add(time.Hour))

	tests := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{name: "валидный", token: valid},
		{name: "истёкший", token: expired, wantErr: true},
		{name: "чужая подпись", token: foreign, wantErr: true},
		{
			// Атака alg=none: токен без подписи должен отклоняться.
			name: "без подписи",
			token: sign(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType,
				jwt.RegisteredClaims{Subject: userID.String(), ExpiresAt: inAnHour}),
			wantErr: true,
		},
		{
			name:    "без exp",
			token:   sign(jwt.SigningMethodHS256, []byte("secret"), jwt.RegisteredClaims{Subject: userID.String()}),
			wantErr: true,
		},
		{
			name:    "sub не UUID",
			token:   sign(jwt.SigningMethodHS256, []byte("secret"), jwt.RegisteredClaims{Subject: "admin", ExpiresAt: inAnHour}),
			wantErr: true,
		},
		{name: "мусор", token: "not.a.jwt", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tokens.Parse(tt.token)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidToken) {
					t.Fatalf("Parse() error = %v, want ErrInvalidToken", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if got != userID {
				t.Errorf("Parse() = %v, want %v", got, userID)
			}
		})
	}
}

func TestNewRefreshToken(t *testing.T) {
	token, hash := NewRefreshToken()
	other, _ := NewRefreshToken()

	if token == other {
		t.Error("два токена подряд совпали")
	}
	if hash != HashRefreshToken(token) {
		t.Error("хеш не совпадает с HashRefreshToken(token)")
	}
	if hash == token {
		t.Error("в базу ушёл бы сам токен")
	}
}
