package config

import (
	"testing"
	"time"
)

func TestRedisSettings(t *testing.T) {
	base := map[string]string{"JWT_SECRET": "jwt", "BFF_API_KEY": "bff", "REDIS_HOST": "localhost", "REDIS_PORT": "6380", "REDIS_PASSWORD": "local", "REDIS_DB": "2"}
	c, err := LoadFrom(settings(base))
	if err != nil {
		t.Fatal(err)
	}
	if c.RedisHost != "localhost" || c.RedisPort != 6380 || c.RedisDB != 2 || c.RedisPassword != "local" || c.RedisDialTimeout != 2*time.Second || c.RedisOperationTimeout != time.Second {
		t.Fatal("redis settings lost")
	}
	for _, tc := range []struct{ k, v string }{{"REDIS_PORT", "0"}, {"REDIS_PORT", "65536"}, {"REDIS_DB", "-1"}, {"REDIS_DB", "wrong"}, {"REDIS_HOST", " "}, {"REDIS_DIAL_TIMEOUT", "bad"}, {"REDIS_OPERATION_TIMEOUT", "0s"}} {
		t.Run(tc.k+tc.v, func(t *testing.T) {
			m := map[string]string{}
			for k, v := range base {
				m[k] = v
			}
			m[tc.k] = tc.v
			if _, err := LoadFrom(settings(m)); err == nil {
				t.Fatal("invalid redis config accepted")
			}
		})
	}
}
