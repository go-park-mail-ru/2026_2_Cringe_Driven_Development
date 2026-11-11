package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLogLevel(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  slog.Level
	}{{"", slog.LevelInfo}, {"debug", slog.LevelDebug}, {"info", slog.LevelInfo}, {"warn", slog.LevelWarn}, {"error", slog.LevelError}} {
		t.Run(tc.value, func(t *testing.T) {
			values := map[string]string{"JWT_SECRET": "jwt", "BFF_API_KEY": "bff-test-key"}
			if tc.value != "" {
				values["LOG_LEVEL"] = tc.value
			}
			got, err := LoadFrom(withFakeRuntime(values))
			if err != nil || got.LogLevel != tc.want {
				t.Fatalf("level=%v err=%v", got.LogLevel, err)
			}
		})
	}
	values := map[string]string{"JWT_SECRET": "jwt", "BFF_API_KEY": "bff-test-key", "LOG_LEVEL": "trace-SECRET"}
	_, err := LoadFrom(withFakeRuntime(values))
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("invalid level error=%v", err)
	}
}

func settings(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		if v, ok := values[key]; ok {
			return v, true
		}
		switch key {
		case "TOTP_ENCRYPTION_KEY":
			return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", true
		case "S3_AVATARS_BUCKET":
			return "avatars", true
		case "S3_AVATARS_PUBLIC_URL":
			return "https://avatars.example.com", true
		}
		return "", false
	}
}
func TestLoadDefaultsAndOverrides(t *testing.T) {
	values := map[string]string{"JWT_SECRET": "jwt", "BFF_API_KEY": "bff-test-key"}
	c, err := LoadFrom(withFakeRuntime(values))
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "0.0.0.0" || c.Port != "8080" || c.AccessTTL != 15*time.Minute || c.RefreshTTL != 720*time.Hour || c.WriteTimeout != 15*time.Second {
		t.Fatalf("unexpected defaults: host=%s port=%s ttl=%s", c.Host, c.Port, c.AccessTTL)
	}
	values["PORT"] = "9000"
	values["ACCESS_TOKEN_TTL"] = "5m"
	values["WRITE_TIMEOUT"] = "invalid"
	c, err = LoadFrom(withFakeRuntime(values))
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != "9000" || c.AccessTTL != 5*time.Minute || c.WriteTimeout != 15*time.Second {
		t.Fatal("overrides or duration fallback changed")
	}
}
func TestMissingRequiredSettings(t *testing.T) {
	for _, key := range []string{"JWT_SECRET", "BFF_API_KEY"} {
		t.Run(key, func(t *testing.T) {
			v := map[string]string{"JWT_SECRET": "jwt", "BFF_API_KEY": "bff-test-key"}
			delete(v, key)
			_, err := LoadFrom(withFakeRuntime(v))
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("missing %s: %v", key, err)
			}
		})
	}
}
func TestPostgresURLCredentials(t *testing.T) {
	c := Config{DBUser: "app@user", DBPassword: "p@ss:word?#%$&", DBHost: "postgres", DBName: "app_db"}
	p, err := pgxpool.ParseConfig(c.PostgresURL())
	if err != nil {
		t.Fatal(err)
	}
	if p.ConnConfig.User != c.DBUser || p.ConnConfig.Password != c.DBPassword || p.ConnConfig.Host != c.DBHost || p.ConnConfig.Database != c.DBName || p.ConnConfig.Port != 5432 {
		t.Fatal("connection parameters changed")
	}
}

// withFakeRuntime передаёт явный валидный runtime-ввод в тесты несвязанных настроек.
func withFakeRuntime(values map[string]string) func(string) (string, bool) {
	full := map[string]string{"RUNTIME_PROVIDER": "fake", "RUNTIME_SECRETS_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	for key, value := range values {
		full[key] = value
	}
	return settings(full)
}
