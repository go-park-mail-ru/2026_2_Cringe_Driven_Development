package config

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// loadRedis читает адрес, таймауты и параметры доступа к Redis.
func (c *Config) loadRedis(lookup func(string) (string, bool)) error {
	get := func(key, fallback string) string {
		if v, ok := lookup(key); ok {
			return v
		}
		return fallback
	}
	c.RedisHost = get("REDIS_HOST", "redis")
	if strings.TrimSpace(c.RedisHost) == "" || strings.TrimSpace(c.RedisHost) != c.RedisHost || strings.ContainsAny(c.RedisHost, "/ \t\r\n") {
		return errors.New("invalid REDIS_HOST")
	}
	port, err := strconv.Atoi(get("REDIS_PORT", "6379"))
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid REDIS_PORT")
	}
	c.RedisPort = port
	db, err := strconv.Atoi(get("REDIS_DB", "0"))
	if err != nil || db < 0 {
		return errors.New("invalid REDIS_DB")
	}
	c.RedisDB = db
	c.RedisPassword = get("REDIS_PASSWORD", "")
	for _, v := range []struct {
		key      string
		fallback string
		dst      *time.Duration
	}{{"REDIS_DIAL_TIMEOUT", "2s", &c.RedisDialTimeout}, {"REDIS_OPERATION_TIMEOUT", "1s", &c.RedisOperationTimeout}} {
		d, e := time.ParseDuration(get(v.key, v.fallback))
		if e != nil || d <= 0 {
			return errors.New("invalid " + v.key)
		}
		*v.dst = d
	}
	return nil
}
