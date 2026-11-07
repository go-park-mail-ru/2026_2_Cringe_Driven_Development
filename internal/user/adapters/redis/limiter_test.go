package redis

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

func TestAttemptLimiterAcrossInstances(t *testing.T) {
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR is not set")
	}
	client := goredis.NewClient(&goredis.Options{Addr: addr, Password: os.Getenv("REDIS_TEST_PASSWORD"), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	prefix := "test:totp:" + uuid.NewString() + ":"
	a := NewAttemptLimiter(client, prefix, time.Second)
	b := NewAttemptLimiter(client, prefix, time.Second)
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			l := a
			if i%2 == 0 {
				l = b
			}
			err := l.Allow(ctx, "confirm:7", 5)
			if err == nil {
				admitted.Add(1)
			} else if !errors.Is(err, usecase.ErrRateLimited) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if admitted.Load() != 5 {
		t.Fatalf("admitted %d", admitted.Load())
	}
	if err := a.Allow(ctx, "setup:7", 30); err != nil {
		t.Fatal(err)
	}
	if err := a.Allow(ctx, "confirm:7", 5); !errors.Is(err, usecase.ErrRateLimited) {
		t.Fatalf("setup reset confirmation limit: %v", err)
	}
	ttl, err := client.PTTL(ctx, prefix+"auth_attempt:confirm:7").Result()
	if err != nil || ttl <= 0 || ttl > time.Minute {
		t.Fatalf("ttl=%v err=%v", ttl, err)
	}
	if err = client.Del(ctx, prefix+"auth_attempt:confirm:7", prefix+"auth_attempt:setup:7").Err(); err != nil {
		t.Fatal(err)
	}
}
