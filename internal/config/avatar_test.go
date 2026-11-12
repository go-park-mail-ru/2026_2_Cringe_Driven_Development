package config

import (
	"strings"
	"testing"
)

func TestAvatarConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, bucket, base string
		valid              bool
	}{
		{"https", "avatars", "https://avatars.example.com/prefix/", true},
		{"local", "avatars", "http://localhost:8333/avatars", true},
		{"missing bucket", "", "https://avatars.example.com", false},
		{"missing url", "avatars", "", false},
		{"relative url", "avatars", "/avatars", false},
		{"credentials", "avatars", "https://user:SECRET@example.com", false},
		{"query", "avatars", "https://example.com?token=SECRET", false},
		{"fragment", "avatars", "https://example.com/#SECRET", false},
		{"scheme", "avatars", "ftp://example.com", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := runtimeBase()
			values["S3_AVATARS_BUCKET"] = tc.bucket
			values["S3_AVATARS_PUBLIC_URL"] = tc.base
			c, err := LoadFrom(withFakeRuntime(values))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if err != nil && strings.Contains(err.Error(), "SECRET") {
				t.Fatal("configuration error leaked URL")
			}
			if err == nil && (c.AvatarsBucket != tc.bucket || c.AvatarsPublicURL != strings.TrimRight(tc.base, "/")) {
				t.Fatalf("avatar config: bucket=%s url=%s", c.AvatarsBucket, c.AvatarsPublicURL)
			}
		})
	}
}
