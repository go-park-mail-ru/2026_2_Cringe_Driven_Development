package security

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"image/png"
	"net/url"
	"strconv"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// TOTP реализует формат аутентификатора Cellestial и шифрование с привязкой к аккаунту.
type TOTP struct{ aead cipher.AEAD }

// NewTOTP требует выделенный 256-битный ключ шифрования.
func NewTOTP(key []byte) (*TOTP, error) {
	if len(key) != 32 {
		return nil, errors.New("TOTP encryption key must contain 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &TOTP{aead: aead}, nil
}

// NewSecret генерирует 160 случайных бит в Base32 без паддинга.
func (*TOTP) NewSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

// Setup отдаёт один и тот же секрет для сканирования QR и ручного ввода.
func (*TOTP) Setup(secret, login string) (usecase.TwoFactorSetup, error) {
	uri := url.URL{Scheme: "otpauth", Host: "totp", Path: "/Cellestial:" + login}
	uri.RawQuery = url.Values{"secret": {secret}, "issuer": {"Cellestial"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}.Encode()
	key, err := otp.NewKeyFromURL(uri.String())
	if err != nil {
		return usecase.TwoFactorSetup{}, errors.New("construct TOTP setup")
	}
	img, err := key.Image(384, 384)
	if err != nil {
		return usecase.TwoFactorSetup{}, errors.New("render TOTP QR")
	}
	var out bytes.Buffer
	if err = png.Encode(&out, img); err != nil {
		return usecase.TwoFactorSetup{}, errors.New("encode TOTP QR")
	}
	return usecase.TwoFactorSetup{Secret: secret, QRCode: "data:image/png;base64," + base64.StdEncoding.EncodeToString(out.Bytes())}, nil
}

func totpAAD(id int64) []byte { return []byte("cellestial:totp:v1:" + strconv.FormatInt(id, 10)) }

// Encrypt включает версию формата и свежий nonce в каждый шифротекст.
func (s *TOTP) Encrypt(id int64, secret string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	prefix := append([]byte{1}, nonce...)
	return s.aead.Seal(prefix, nonce, []byte(secret), totpAAD(id)), nil
}

// Decrypt отклоняет шифротекст другого аккаунта или другого ключа шифрования.
func (s *TOTP) Decrypt(id int64, ciphertext []byte) (string, error) {
	n := s.aead.NonceSize()
	if len(ciphertext) < 1+n+s.aead.Overhead() || ciphertext[0] != 1 {
		return "", errors.New("invalid encrypted TOTP secret")
	}
	plain, err := s.aead.Open(nil, ciphertext[1:1+n], ciphertext[1+n:], totpAAD(id))
	if err != nil {
		return "", errors.New("decrypt TOTP secret")
	}
	return string(plain), nil
}

// Match возвращает совпавший шаг, чтобы хранилище могло атомарно предотвратить повтор.
func (*TOTP) Match(secret, code string, now time.Time) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	for i := range len(code) {
		if code[i] < '0' || code[i] > '9' {
			return 0, false
		}
	}
	step := now.Unix() / 30
	for candidate := step + 1; candidate >= step-1; candidate-- {
		if candidate < 0 {
			continue
		}
		want, err := totp.GenerateCodeCustom(secret, time.Unix(candidate*30, 0), totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return candidate, true
		}
	}
	return 0, false
}
