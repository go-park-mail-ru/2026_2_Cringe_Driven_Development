// Package config читает настройки API из окружения.
package config

import (
	"errors"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	RedisHost, RedisPassword                string
	RedisPort, RedisDB                      int
	RedisDialTimeout, RedisOperationTimeout time.Duration
	BFFAPIKey                               string
	LogLevel                                slog.Level
	Host, Port                              string
	WriteTimeout, ReadTimeout               time.Duration
	JWTSecret                               string
	AccessTTL, RefreshTTL                   time.Duration
	NotebooksBucket                         string
	AvatarsBucket, AvatarsPublicURL         string
	DBUser, DBPassword, DBHost, DBName      string
	Runtime                                 RuntimeConfig
}

// Load читает окружение процесса.
func Load() (Config, error) { return LoadFrom(os.LookupEnv) }

// LoadFrom позволяет получить настройки без изменения окружения процесса.
func LoadFrom(lookup func(string) (string, bool)) (Config, error) {
	get := func(key, fallback string) string {
		if value, ok := lookup(key); ok {
			return value
		}
		return fallback
	}
	duration := func(key string, fallback time.Duration) time.Duration {
		if value, ok := lookup(key); ok {
			if d, err := time.ParseDuration(value); err == nil {
				return d
			}
			slog.Warn("invalid duration in env, using fallback", slog.String("key", key))
		}
		return fallback
	}
	c := Config{
		BFFAPIKey: get("BFF_API_KEY", ""),
		LogLevel:  slog.LevelInfo,
		Host:      get("HOST", "0.0.0.0"), Port: get("PORT", "8080"),
		WriteTimeout: duration("WRITE_TIMEOUT", 15*time.Second), ReadTimeout: duration("READ_TIMEOUT", 15*time.Second),
		JWTSecret: get("JWT_SECRET", ""),
		AccessTTL: duration("ACCESS_TOKEN_TTL", 15*time.Minute), RefreshTTL: duration("REFRESH_TOKEN_TTL", 720*time.Hour),
		NotebooksBucket: get("S3_NOTEBOOKS_BUCKET", ""),
		DBUser:          get("POSTGRES_USER", "postgres"), DBPassword: get("POSTGRES_PASSWORD", "postgres"), DBHost: get("POSTGRES_HOST", "db"), DBName: get("POSTGRES_DB", "app_db"),
	}
	if raw, ok := lookup("LOG_LEVEL"); ok {
		switch raw {
		case "debug":
			c.LogLevel = slog.LevelDebug
		case "info":
			c.LogLevel = slog.LevelInfo
		case "warn":
			c.LogLevel = slog.LevelWarn
		case "error":
			c.LogLevel = slog.LevelError
		default:
			return Config{}, errors.New("invalid LOG_LEVEL: expected debug, info, warn or error")
		}
	}
	if strings.TrimSpace(c.BFFAPIKey) == "" {
		return Config{}, errors.New("BFF_API_KEY is not set")
	}
	if c.AccessTTL < time.Second || c.RefreshTTL < time.Second {
		return Config{}, errors.New("token TTL must be at least one second")
	}
	if c.JWTSecret == "" {
		return Config{}, errors.New("JWT_SECRET is not set")
	}
	if c.NotebooksBucket == "" {
		return Config{}, errors.New("S3_NOTEBOOKS_BUCKET is not set")
	}
	c.AvatarsBucket = strings.TrimSpace(get("S3_AVATARS_BUCKET", ""))
	c.AvatarsPublicURL = strings.TrimRight(get("S3_AVATARS_PUBLIC_URL", ""), "/")
	if c.AvatarsBucket == "" {
		return Config{}, errors.New("S3_AVATARS_BUCKET is not set")
	}
	avatarURL, parseErr := url.Parse(c.AvatarsPublicURL)
	if parseErr != nil || avatarURL.Hostname() == "" || (avatarURL.Scheme != "https" && avatarURL.Scheme != "http") || avatarURL.User != nil || avatarURL.RawQuery != "" || avatarURL.ForceQuery || avatarURL.Fragment != "" || strings.Contains(c.AvatarsPublicURL, "#") {
		return Config{}, errors.New("invalid S3_AVATARS_PUBLIC_URL: expected absolute HTTP(S) URL without credentials, query or fragment")
	}
	if err := c.loadRedis(lookup); err != nil {
		return Config{}, err
	}
	rt, err := loadRuntime(lookup)
	if err != nil {
		return Config{}, err
	}
	c.Runtime = rt
	return c, nil
}

// PostgresURL сохраняет специальные символы в имени и пароле.
func (c Config) PostgresURL() string {
	u := url.URL{Scheme: "postgres", User: url.UserPassword(c.DBUser, c.DBPassword), Host: c.DBHost + ":5432", Path: "/" + c.DBName}
	return u.String()
}
