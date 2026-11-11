package security

import (
	"bytes"
	"encoding/base32"
	"encoding/base64"
	"image/png"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
)

func TestSetupQRMatchesManualSecret(t *testing.T) {
	s, err := NewTOTP(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	secret, err := s.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 32 || strings.ContainsAny(secret, "= -") {
		t.Fatal("invalid Base32 secret")
	}
	setup, err := s.Setup(secret, "alice_123")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(setup.QRCode, "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	bitmap, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := qrcode.NewQRCodeReader().Decode(bitmap, nil)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(decoded.GetText())
	if err != nil {
		t.Fatal(err)
	}
	if uri.Scheme != "otpauth" || uri.Host != "totp" || uri.Path != "/Cellestial:alice_123" {
		t.Fatal("invalid authenticator account")
	}
	for key, want := range map[string]string{"secret": setup.Secret, "issuer": "Cellestial", "algorithm": "SHA1", "digits": "6", "period": "30"} {
		if uri.Query().Get(key) != want {
			t.Fatalf("QR parameter %s does not match manual setup", key)
		}
	}
}

func TestTOTPEncryptionAndRFC6238(t *testing.T) {
	s, err := NewTOTP(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	// Вектор RFC 6238 SHA-1 94287082, обрезанный до обязательных шести цифр.
	step, ok := s.Match(secret, "287082", time.Unix(59, 0))
	if !ok || step != 1 {
		t.Fatalf("step=%d valid=%v", step, ok)
	}
	if _, valid := s.Match(secret, "005924", time.Unix(1234567890, 0)); !valid {
		t.Fatal("RFC code with leading zeroes rejected")
	}
	if _, ok = s.Match(secret, "287082", time.Unix(89, 0)); !ok {
		t.Fatal("previous step rejected")
	}
	if _, ok = s.Match(secret, "287082", time.Unix(119, 0)); ok {
		t.Fatal("outside time window accepted")
	}
	for _, code := range []string{"28708", "２８７０８２", "287082\n", "abcdef"} {
		if _, ok = s.Match(secret, code, time.Unix(59, 0)); ok {
			t.Fatalf("accepted %q", code)
		}
	}
	ciphertext, err := s.Encrypt(7, secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(secret)) {
		t.Fatal("plaintext secret stored")
	}
	plain, err := s.Decrypt(7, ciphertext)
	if err != nil || plain != secret {
		t.Fatal("secret roundtrip failed")
	}
	if _, err = s.Decrypt(8, ciphertext); err == nil {
		t.Fatal("wrong account accepted")
	}
	other, _ := NewTOTP(bytes.Repeat([]byte{2}, 32))
	if _, err = other.Decrypt(7, ciphertext); err == nil {
		t.Fatal("wrong key accepted")
	}
	ciphertext[len(ciphertext)-1] ^= 1
	if _, err = s.Decrypt(7, ciphertext); err == nil {
		t.Fatal("tampered secret accepted")
	}
}
