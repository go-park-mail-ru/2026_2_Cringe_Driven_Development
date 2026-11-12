package config

import (
	"strings"
	"testing"
)

func TestMandatoryBFFKey(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		wantErr   bool
	}{
		{"missing", "", true},
		{"whitespace", " \t", true},
		{"current", "current-test-key", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{"JWT_SECRET": "jwt", "BFF_API_KEY": tc.key}
			c, err := LoadFrom(withFakeRuntime(values))
			if (err != nil) != tc.wantErr {
				t.Fatalf("unexpected configuration error: %v", err)
			}
			if err == nil && c.BFFAPIKey != tc.key {
				t.Fatal("configured key was changed")
			}
			if err != nil && strings.Contains(err.Error(), "current-test-key") {
				t.Fatal("error leaked key")
			}
		})
	}
}

func TestBFFStartupNeedsNoBrowserSecrets(t *testing.T) {
	_, err := LoadFrom(withFakeRuntime(map[string]string{"JWT_SECRET": "jwt", "BFF_API_KEY": "current-test-key"}))
	if err != nil {
		t.Fatalf("server contract must not require browser configuration: %v", err)
	}
}
