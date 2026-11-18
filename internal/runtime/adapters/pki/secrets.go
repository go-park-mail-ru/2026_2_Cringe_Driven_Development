// Package pki генерирует токены, шифрует секреты и выпускает сертификаты VPS.
package pki

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"golang.org/x/crypto/ssh"
)

const certTTL = 7 * 24 * time.Hour

// Secrets реализует usecase.Secrets.
type Secrets struct {
	aead   cipher.AEAD
	caCert *x509.Certificate
	caKey  crypto.Signer
	clock  usecase.Clock
}

var _ usecase.Secrets = (*Secrets)(nil)

// New создаёт Secrets: key — 32 байта AES-256, CA — PEM сертификат и ключ.
func New(key []byte, caCertPEM, caKeyPEM []byte, clock usecase.Clock) (*Secrets, error) {
	if len(key) != 32 {
		return nil, errors.New("pki: encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("pki: init cipher")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("pki: init gcm")
	}
	cb, _ := pem.Decode(caCertPEM)
	if cb == nil {
		return nil, errors.New("pki: CA certificate is not valid PEM")
	}
	caCert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, errors.New("pki: parse CA certificate")
	}
	kb, _ := pem.Decode(caKeyPEM)
	if kb == nil {
		return nil, errors.New("pki: CA key is not valid PEM")
	}
	caKey, err := parseSigner(kb.Bytes)
	if err != nil {
		return nil, err
	}
	if !caCert.BasicConstraintsValid || !caCert.IsCA {
		return nil, errors.New("pki: CA certificate is not a CA")
	}
	pub, ok := caKey.Public().(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !pub.Equal(caCert.PublicKey) {
		return nil, errors.New("pki: CA key does not match CA certificate")
	}
	return &Secrets{aead: aead, caCert: caCert, caKey: caKey, clock: clock}, nil
}

// parseSigner загружает ключ подписи сертификатов.
func parseSigner(der []byte) (crypto.Signer, error) {
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		switch s := k.(type) {
		case *ecdsa.PrivateKey:
			return s, nil
		case ed25519.PrivateKey:
			return s, nil
		case *rsa.PrivateKey:
			return s, nil
		}
		return nil, errors.New("pki: unsupported CA key type")
	}
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	return nil, errors.New("pki: parse CA key")
}

// NewToken возвращает 32 случайных байта в hex.
func (s *Secrets) NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("pki: random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Seal шифрует AES-256-GCM; nonce записан в начало результата.
func (s *Secrets) Seal(p []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("pki: random: %w", err)
	}
	return s.aead.Seal(nonce, nonce, p, nil), nil
}

// Open расшифровывает результат Seal.
func (s *Secrets) Open(c []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(c) < n+s.aead.Overhead() {
		return nil, errors.New("pki: ciphertext too short")
	}
	p, err := s.aead.Open(nil, c[:n], c[n:], nil)
	if err != nil {
		return nil, errors.New("pki: decrypt failed")
	}
	return p, nil
}

// NewHostKey генерирует ed25519 ключ хоста: приватный PEM (OpenSSH) и строку authorized_keys.
func (s *Secrets) NewHostKey() ([]byte, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("pki: generate host key: %w", err)
	}
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, "", errors.New("pki: marshal host key")
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, "", errors.New("pki: public host key")
	}
	line := string(ssh.MarshalAuthorizedKey(sp))
	return pem.EncodeToMemory(blk), line[:len(line)-1], nil
}

// IssueServerCert выпускает серверный сертификат на IP, подписанный CA.
func (s *Secrets) IssueServerCert(ip string) ([]byte, []byte, error) {
	addr := net.ParseIP(ip)
	if addr == nil {
		return nil, nil, fmt.Errorf("pki: invalid ip %q", ip)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, fmt.Errorf("pki: serial: %w", err)
	}
	now := s.clock.Now()
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: ip},
		IPAddresses:  []net.IP{addr},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(certTTL),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, s.caCert, &key.PublicKey, s.caKey)
	if err != nil {
		return nil, nil, errors.New("pki: sign certificate")
	}
	kd, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, errors.New("pki: marshal key")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}), nil
}
