package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	goredis "github.com/redis/go-redis/v9"
)

// AttemptLimiter разделяет минутные бюджеты попыток между процессами API.
type AttemptLimiter struct {
	client  *goredis.Client
	prefix  string
	timeout time.Duration
}

// NewAttemptLimiter использует тот же primary Redis без ретраев, что и сессии авторизации.
func NewAttemptLimiter(client *goredis.Client, prefix string, timeout time.Duration) *AttemptLimiter {
	return &AttemptLimiter{client: client, prefix: prefix, timeout: timeout}
}

// Allow резервирует попытку. Недоступное хранилище никогда не разрешает проверку пароля/TOTP.
func (l *AttemptLimiter) Allow(ctx context.Context, key string, limit int) error {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	const script = `
local n = tonumber(redis.call('GET', KEYS[1]) or '0')
if n >= tonumber(ARGV[1]) then return 0 end
redis.call('INCR', KEYS[1])
if n == 0 then redis.call('PEXPIRE', KEYS[1], 60000) end
return 1`
	allowed, err := l.client.Eval(ctx, script, []string{l.prefix + "auth_attempt:" + key}, limit).Int()
	if err != nil {
		return fmt.Errorf("reserve authentication attempt: %w", err)
	}
	if allowed != 1 {
		return usecase.ErrRateLimited
	}
	return nil
}
