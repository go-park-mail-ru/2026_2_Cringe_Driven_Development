package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestTOTPKeyRequiredAndValidated(t *testing.T) {
	for _, value := range []string{"", "not-a-key", base64.StdEncoding.EncodeToString(make([]byte, 31))} {
		_, err := LoadFrom(withFakeRuntime(map[string]string{"JWT_SECRET": "jwt", "BFF_API_KEY": "bff", "TOTP_ENCRYPTION_KEY": value}))
		if err == nil || !strings.Contains(err.Error(), "TOTP_ENCRYPTION_KEY") {
			t.Fatalf("invalid key accepted: %v", err)
		}
	}
	c, err := LoadFrom(withFakeRuntime(map[string]string{"JWT_SECRET": "jwt", "BFF_API_KEY": "bff", "TOTP_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32))}))
	if err != nil || len(c.TOTPEncryptionKey) != 32 {
		t.Fatalf("valid key rejected: %v", err)
	}
}
