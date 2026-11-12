package redis

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// Теряем ответ только после того, как Redis действительно выполнил команду. Это моделирует
// неоднозначный исход записи, а не предполагает, что любая сетевая ошибка откатывает её.
type lostReplyConn struct {
	net.Conn
	drop   bool
	writes *atomic.Int32
}

func (c *lostReplyConn) Write(b []byte) (int, error) {
	if bytes.Contains(b, []byte("\r\neval\r\n")) {
		c.drop = true
		c.writes.Add(1)
	}
	return c.Conn.Write(b)
}
func (c *lostReplyConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if c.drop && n > 0 {
		return 0, io.ErrUnexpectedEOF
	}
	return n, err
}
func TestLostRotationReplyIsUnknownOutcomeAndNeverRetried(t *testing.T) {
	s, c, ctx := fixture(t)
	a := newSession(ctx, t, s, 7)
	must(t, s.Create(ctx, a))
	r := a
	r.TokenHash = uuid.NewString()
	opts := *c.Options()
	opts.MaxRetries = -1
	opts.ContextTimeoutEnabled = true
	var writes atomic.Int32
	opts.Dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &lostReplyConn{Conn: conn, writes: &writes}, nil
	}
	lossy := goredis.NewClient(&opts)
	defer func() { _ = lossy.Close() }()
	store := NewStore(lossy, s.prefix, time.Second)
	err := store.Rotate(ctx, a.TokenHash, r)
	if err == nil || errors.Is(err, usecase.ErrInvalidSession) {
		t.Fatalf("lost reply reported as success/invalid: %v", err)
	}
	if writes.Load() != 1 {
		t.Fatalf("mutation retried %d times", writes.Load())
	}
	_, err = s.Find(ctx, a.TokenHash)
	invalid(t, err)
	_, err = s.Find(ctx, r.TokenHash)
	must(t, err)
	must(t, s.ValidateAccess(ctx, a.SessionClaims))
}

func TestOperationDeadlineBoundsUnresponsiveRedis(t *testing.T) {
	// Пир принимает записи, но никогда не отвечает, в отличие от отклонённого соединения.
	c := goredis.NewClient(&goredis.Options{Addr: "unused:6379", MaxRetries: -1, ContextTimeoutEnabled: true,
		DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, PoolTimeout: time.Second,
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			client, peer := net.Pipe()
			go func() { defer func() { _ = peer.Close() }(); _, _ = io.Copy(io.Discard, peer) }()
			return client, nil
		},
	})
	defer func() { _ = c.Close() }()
	s := NewStore(c, "test:", 50*time.Millisecond)
	started := time.Now()
	err := s.ValidateAccess(context.Background(), usecase.SessionClaims{UserID: 7, SessionID: "sid", Version: 1})
	if err == nil || errors.Is(err, usecase.ErrInvalidSession) {
		t.Fatalf("timeout returned success/invalid: %v", err)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("operation ignored its deadline")
	}
}
