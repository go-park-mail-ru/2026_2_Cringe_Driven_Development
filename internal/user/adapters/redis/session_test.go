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

func fixture(t *testing.T) (*Store, *goredis.Client, context.Context) {
	t.Helper()
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("set REDIS_TEST_ADDR to run real Redis integration tests")
	}
	c := goredis.NewClient(&goredis.Options{Addr: addr, Password: os.Getenv("REDIS_TEST_PASSWORD"), MaxRetries: -1})
	ctx := context.Background()
	if err := c.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	prefix := "test:" + uuid.NewString() + ":"
	s := NewStore(c, prefix, time.Second)
	t.Cleanup(func() {
		var cursor uint64
		for {
			keys, next, err := c.Scan(ctx, cursor, prefix+"*", 100).Result()
			if err != nil {
				t.Error(err)
				break
			}
			if len(keys) > 0 {
				if err := c.Del(ctx, keys...).Err(); err != nil {
					t.Error(err)
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
		_ = c.Close()
	})
	return s, c, ctx
}
func newSession(ctx context.Context, t *testing.T, s *Store, user int64) usecase.NewSession {
	t.Helper()
	v, err := s.CurrentVersion(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	return usecase.NewSession{RefreshSession: usecase.RefreshSession{SessionClaims: usecase.SessionClaims{UserID: user, SessionID: uuid.NewString(), Version: v}, TokenHash: uuid.NewString(), ExpiresAt: time.Now().Add(time.Minute)}, AccessExpiresAt: time.Now().Add(2 * time.Minute)}
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func invalid(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, usecase.ErrInvalidSession) {
		t.Fatalf("want invalid session, got %v", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s, c, ctx := fixture(t)
	a := newSession(ctx, t, s, 7)
	b := newSession(ctx, t, s, 7)
	other := newSession(ctx, t, s, 8)
	for _, v := range []usecase.NewSession{a, b, other} {
		must(t, s.Create(ctx, v))
	}
	ttl := c.PTTL(ctx, s.authKey(a.SessionID)).Val()
	found, err := s.Find(ctx, a.TokenHash)
	must(t, err)
	if found.SessionClaims != a.SessionClaims {
		t.Fatal(found)
	}
	must(t, s.ValidateAccess(ctx, a.SessionClaims))
	after := c.PTTL(ctx, s.authKey(a.SessionID)).Val()
	if after > ttl || after < ttl-time.Second {
		t.Fatalf("read changed TTL %s -> %s", ttl, after)
	}
	r := a
	r.TokenHash = uuid.NewString()
	r.ExpiresAt = time.Now().Add(3 * time.Minute)
	r.AccessExpiresAt = time.Now().Add(time.Minute)
	must(t, s.Rotate(ctx, a.TokenHash, r))
	invalid(t, func() error { _, e := s.Find(ctx, a.TokenHash); return e }())
	must(t, s.ValidateAccess(ctx, a.SessionClaims))
	invalid(t, s.Revoke(ctx, r.TokenHash, b.SessionClaims))
	must(t, s.ValidateAccess(ctx, a.SessionClaims))
	must(t, s.Revoke(ctx, r.TokenHash, a.SessionClaims))
	invalid(t, s.ValidateAccess(ctx, a.SessionClaims))
	invalid(t, s.Revoke(ctx, r.TokenHash, a.SessionClaims))
	must(t, s.ValidateAccess(ctx, b.SessionClaims))
	must(t, s.RevokeAll(ctx, 7))
	invalid(t, s.ValidateAccess(ctx, b.SessionClaims))
	_, err = s.Find(ctx, b.TokenHash)
	invalid(t, err)
	must(t, s.ValidateAccess(ctx, other.SessionClaims))
	_, err = s.Find(ctx, other.TokenHash)
	must(t, err)
	if err := s.Create(ctx, a); !errors.Is(err, usecase.ErrVersionChanged) {
		t.Fatal(err)
	}
	fresh := newSession(ctx, t, s, 7)
	must(t, s.Create(ctx, fresh))
	must(t, s.ValidateAccess(ctx, fresh.SessionClaims))
}
func TestConcurrentRotationAndVersionInitialization(t *testing.T) {
	s, _, ctx := fixture(t)
	var wg sync.WaitGroup
	versions := make(chan usecase.SessionVersion, 32)
	for range 32 {
		wg.Go(func() {
			v, e := s.CurrentVersion(ctx, 7)
			if e != nil {
				t.Error(e)
			}
			versions <- v
		})
	}
	wg.Wait()
	close(versions)
	var first usecase.SessionVersion
	for v := range versions {
		if v == 0 {
			t.Fatal("zero version")
		}
		if first == 0 {
			first = v
		}
		if v != first {
			t.Fatal("initialization race")
		}
	}
	a := newSession(ctx, t, s, 7)
	must(t, s.Create(ctx, a))
	var wins atomic.Int32
	for range 32 {
		wg.Go(func() {
			r := a
			r.TokenHash = uuid.NewString()
			err := s.Rotate(ctx, a.TokenHash, r)
			if err == nil {
				wins.Add(1)
			} else if !errors.Is(err, usecase.ErrInvalidSession) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal(wins.Load())
	}
}
func TestRefreshExpiryDoesNotExpireAccess(t *testing.T) {
	s, c, ctx := fixture(t)
	a := newSession(ctx, t, s, 7)
	a.ExpiresAt = time.Now().Add(80 * time.Millisecond)
	a.AccessExpiresAt = time.Now().Add(time.Minute)
	must(t, s.Create(ctx, a))
	time.Sleep(120 * time.Millisecond)
	_, err := s.Find(ctx, a.TokenHash)
	invalid(t, err)
	must(t, s.ValidateAccess(ctx, a.SessionClaims))
	if c.Exists(ctx, s.refreshKey(a.TokenHash)).Val() != 0 {
		t.Fatal("expired refresh retained")
	}
	before := c.PTTL(ctx, s.authKey(a.SessionID)).Val()
	if before < 50*time.Second {
		t.Fatal(before)
	}
	must(t, s.RevokeAll(ctx, 7))
	invalid(t, s.ValidateAccess(ctx, a.SessionClaims))
}
func TestMissingVersionAndExactIncrement(t *testing.T) {
	s, c, ctx := fixture(t)
	a := newSession(ctx, t, s, 7)
	must(t, s.Create(ctx, a))
	must(t, c.Del(ctx, s.versionKey(7)).Err())
	_, err := s.Find(ctx, a.TokenHash)
	invalid(t, err)
	invalid(t, s.ValidateAccess(ctx, a.SessionClaims))
	if c.Exists(ctx, s.versionKey(7)).Val() != 0 {
		t.Fatal("read reinitialized missing version")
	}
	if err = s.Create(ctx, a); !errors.Is(err, usecase.ErrVersionChanged) {
		t.Fatal(err)
	}
	v, err := s.CurrentVersion(ctx, 7)
	must(t, err)
	if v == a.Version {
		t.Fatal("lost version reused")
	}
	invalid(t, s.ValidateAccess(ctx, a.SessionClaims))
	must(t, c.Set(ctx, s.versionKey(7), "18446744073709551614", 0).Err())
	must(t, s.RevokeAll(ctx, 7))
	if got := c.Get(ctx, s.versionKey(7)).Val(); got != "18446744073709551615" {
		t.Fatal(got)
	}
	if err := s.RevokeAll(ctx, 7); err == nil {
		t.Fatal("overflow accepted")
	}
	if got := c.Get(ctx, s.versionKey(7)).Val(); got != "18446744073709551615" {
		t.Fatal(got)
	}
}

func TestRejectedMutationsPreserveCurrentSession(t *testing.T) {
	for _, tc := range []string{"same hash", "occupied hash", "wrong sid", "wrong owner", "wrong version", "expired refresh", "expired access", "wrong key type"} {
		t.Run(tc, func(t *testing.T) {
			s, c, ctx := fixture(t)
			a := newSession(ctx, t, s, 7)
			must(t, s.Create(ctx, a))
			before := c.Get(ctx, s.authKey(a.SessionID)).Val()
			r := a
			r.TokenHash = uuid.NewString()
			switch tc {
			case "same hash":
				r.TokenHash = a.TokenHash
			case "occupied hash":
				must(t, c.Set(ctx, s.refreshKey(r.TokenHash), "occupied", time.Minute).Err())
			case "wrong sid":
				r.SessionID = uuid.NewString()
			case "wrong owner":
				r.UserID = 8
			case "wrong version":
				r.Version++
			case "expired refresh":
				r.ExpiresAt = time.Now().Add(-time.Second)
			case "expired access":
				r.AccessExpiresAt = time.Now().Add(-time.Second)
			case "wrong key type":
				must(t, c.LPush(ctx, s.refreshKey(r.TokenHash), "occupied").Err())
			}
			invalid(t, s.Rotate(ctx, a.TokenHash, r))
			if after := c.Get(ctx, s.authKey(a.SessionID)).Val(); after != before {
				t.Fatal("failed rotation mutated auth")
			}
			_, err := s.Find(ctx, a.TokenHash)
			must(t, err)
		})
	}
}
func TestRotationNeverShortensAccessRetention(t *testing.T) {
	s, c, ctx := fixture(t)
	a := newSession(ctx, t, s, 7)
	must(t, s.Create(ctx, a))
	oldDeadline := c.PExpireTime(ctx, s.authKey(a.SessionID)).Val()
	r := a
	r.TokenHash = uuid.NewString()
	r.ExpiresAt = time.Now().Add(time.Second)
	r.AccessExpiresAt = time.Now().Add(2 * time.Second)
	must(t, s.Rotate(ctx, a.TokenHash, r))
	if c.PExpireTime(ctx, s.authKey(a.SessionID)).Val() != oldDeadline {
		t.Fatal("access retention shortened")
	}
}
func TestCreateRejectsCollisionsAndInvalidLifetimes(t *testing.T) {
	for _, tc := range []string{"sid", "hash", "expired refresh", "expired access", "wrong version type"} {
		t.Run(tc, func(t *testing.T) {
			s, c, ctx := fixture(t)
			a := newSession(ctx, t, s, 7)
			switch tc {
			case "sid":
				must(t, c.Set(ctx, s.authKey(a.SessionID), "sentinel", time.Minute).Err())
			case "hash":
				must(t, c.LPush(ctx, s.refreshKey(a.TokenHash), "sentinel").Err())
			case "expired refresh":
				a.ExpiresAt = time.Now().Add(-time.Second)
			case "expired access":
				a.AccessExpiresAt = time.Now().Add(-time.Second)
			case "wrong version type":
				must(t, c.Del(ctx, s.versionKey(7)).Err())
				must(t, c.LPush(ctx, s.versionKey(7), "sentinel").Err())
			}
			if err := s.Create(ctx, a); err == nil {
				t.Fatal("invalid creation accepted")
			}
			if tc != "sid" && c.Exists(ctx, s.authKey(a.SessionID)).Val() != 0 {
				t.Fatal("partial auth write")
			}
			if tc != "hash" && c.Exists(ctx, s.refreshKey(a.TokenHash)).Val() != 0 {
				t.Fatal("partial refresh write")
			}
		})
	}
}
func TestRevocationOrdering(t *testing.T) {
	for _, tc := range []string{"logout first", "rotate first", "revoke all first", "rotate then revoke all", "create after revoke all"} {
		t.Run(tc, func(t *testing.T) {
			s, _, ctx := fixture(t)
			a := newSession(ctx, t, s, 7)
			must(t, s.Create(ctx, a))
			r := a
			r.TokenHash = uuid.NewString()
			switch tc {
			case "logout first":
				must(t, s.Revoke(ctx, a.TokenHash, a.SessionClaims))
				invalid(t, s.Rotate(ctx, a.TokenHash, r))
			case "rotate first":
				must(t, s.Rotate(ctx, a.TokenHash, r))
				invalid(t, s.Revoke(ctx, a.TokenHash, a.SessionClaims))
				must(t, s.ValidateAccess(ctx, a.SessionClaims))
				must(t, s.Revoke(ctx, r.TokenHash, a.SessionClaims))
			case "revoke all first":
				must(t, s.RevokeAll(ctx, 7))
				invalid(t, s.Rotate(ctx, a.TokenHash, r))
			case "rotate then revoke all":
				must(t, s.Rotate(ctx, a.TokenHash, r))
				must(t, s.RevokeAll(ctx, 7))
				_, err := s.Find(ctx, r.TokenHash)
				invalid(t, err)
			case "create after revoke all":
				must(t, s.RevokeAll(ctx, 7))
				r.SessionID = uuid.NewString()
				if err := s.Create(ctx, r); !errors.Is(err, usecase.ErrVersionChanged) {
					t.Fatal(err)
				}
			}
			invalid(t, s.ValidateAccess(ctx, a.SessionClaims))
		})
	}
}
func TestConcurrentRevocationDoesNotResurrectSession(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(map[bool]string{false: "logout", true: "all"}[all], func(t *testing.T) {
			s, _, ctx := fixture(t)
			for range 30 {
				a := newSession(ctx, t, s, 7)
				must(t, s.Create(ctx, a))
				r := a
				r.TokenHash = uuid.NewString()
				start := make(chan struct{})
				var rotateErr, revokeErr error
				var wg sync.WaitGroup
				wg.Go(func() { <-start; rotateErr = s.Rotate(ctx, a.TokenHash, r) })
				wg.Go(func() {
					<-start
					if all {
						revokeErr = s.RevokeAll(ctx, 7)
					} else {
						revokeErr = s.Revoke(ctx, a.TokenHash, a.SessionClaims)
					}
				})
				close(start)
				wg.Wait()
				switch {
				case all:
					must(t, revokeErr)
					invalid(t, s.ValidateAccess(ctx, a.SessionClaims))
				case revokeErr == nil:
					invalid(t, rotateErr)
					invalid(t, s.ValidateAccess(ctx, a.SessionClaims))
				default:
					invalid(t, revokeErr)
					must(t, rotateErr)
					must(t, s.Revoke(ctx, r.TokenHash, r.SessionClaims))
				}
			}
		})
	}
}
func TestTTLAndPersistentVersion(t *testing.T) {
	s, c, ctx := fixture(t)
	a := newSession(ctx, t, s, 7)
	a.ExpiresAt = time.Now().Add(50 * time.Millisecond)
	a.AccessExpiresAt = time.Now().Add(50 * time.Millisecond)
	must(t, s.Create(ctx, a))
	time.Sleep(100 * time.Millisecond)
	invalid(t, s.ValidateAccess(ctx, a.SessionClaims))
	if c.Exists(ctx, s.authKey(a.SessionID), s.refreshKey(a.TokenHash)).Val() != 0 {
		t.Fatal("TTL did not remove sessions")
	}
	v, err := s.CurrentVersion(ctx, 7)
	must(t, err)
	if v != a.Version || c.PTTL(ctx, s.versionKey(7)).Val() != -1 {
		t.Fatal("version expired or changed")
	}
	must(t, s.RevokeAll(ctx, 99))
	v, err = s.CurrentVersion(ctx, 99)
	must(t, err)
	if v == 0 {
		t.Fatal("uninitialized revoke all")
	}
}
func TestAccessRejectsMismatchedClaims(t *testing.T) {
	s, _, ctx := fixture(t)
	a := newSession(ctx, t, s, 7)
	must(t, s.Create(ctx, a))
	for _, c := range []usecase.SessionClaims{{UserID: 8, SessionID: a.SessionID, Version: a.Version}, {UserID: 7, SessionID: "other", Version: a.Version}, {UserID: 7, SessionID: a.SessionID, Version: a.Version + 1}, {}} {
		invalid(t, s.ValidateAccess(ctx, c))
		invalid(t, s.Revoke(ctx, a.TokenHash, c))
	}
	must(t, s.ValidateAccess(ctx, a.SessionClaims))
}
func TestStorageFailureIsNotInvalidSession(t *testing.T) {
	s, _, ctx := fixture(t)
	a := newSession(ctx, t, s, 7)
	must(t, s.Create(ctx, a))
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, run := range []func() error{func() error { return s.ValidateAccess(canceled, a.SessionClaims) }, func() error { return s.Rotate(canceled, a.TokenHash, a) }, func() error { return s.Revoke(canceled, a.TokenHash, a.SessionClaims) }, func() error { return s.RevokeAll(canceled, 7) }} {
		err := run()
		if err == nil || errors.Is(err, usecase.ErrInvalidSession) {
			t.Fatalf("wrong infrastructure error: %v", err)
		}
	}
	must(t, s.ValidateAccess(ctx, a.SessionClaims))
	dead := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 50 * time.Millisecond})
	defer func() { _ = dead.Close() }()
	unavailable := NewStore(dead, "", 100*time.Millisecond)
	if err := unavailable.ValidateAccess(ctx, a.SessionClaims); err == nil || errors.Is(err, usecase.ErrInvalidSession) {
		t.Fatal(err)
	}
}

// Полный сброс БД (docs/database-reset.md) очищает Redis приложения: старые access/refresh-токены
// не должны действовать, даже если новый пользователь получил прежний числовой ID.
func TestResetRevokesSessionsOfReusedUserIDs(t *testing.T) {
	s, c, ctx := fixture(t)
	old := newSession(ctx, t, s, 1)
	must(t, s.Create(ctx, old))
	must(t, s.ValidateAccess(ctx, old.SessionClaims))
	// FLUSHDB в пределах ключей этого Store: общий Redis тестов не очищается целиком
	keys, err := c.Keys(ctx, s.prefix+"*").Result()
	must(t, err)
	must(t, c.Del(ctx, keys...).Err())

	invalid(t, s.ValidateAccess(ctx, old.SessionClaims))
	_, err = s.Find(ctx, old.TokenHash)
	invalid(t, err)
	reused := newSession(ctx, t, s, 1)
	must(t, s.Create(ctx, reused))
	must(t, s.ValidateAccess(ctx, reused.SessionClaims))
	invalid(t, s.ValidateAccess(ctx, old.SessionClaims))
	_, err = s.Find(ctx, old.TokenHash)
	invalid(t, err)
}
