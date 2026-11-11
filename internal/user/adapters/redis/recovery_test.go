package redis

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

func resetNamespace(t *testing.T, s *Store, c *goredis.Client) {
	t.Helper()
	host, port, err := net.SplitHostPort(c.Options().Addr)
	must(t, err)
	root, err := filepath.Abs("../../../..")
	must(t, err)
	cmd := exec.Command("bash", filepath.Join(root, "scripts/reset-auth-sessions.sh"))
	cmd.Env = append(os.Environ(), "AUTH_API_STOPPED=yes", "AUTH_REDIS_NAMESPACE="+s.prefix, "REDIS_HOST="+host, "REDIS_PORT="+port, "REDIS_DB="+strconv.Itoa(c.Options().DB), "REDIS_PASSWORD="+c.Options().Password)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("reset failed: %v: %s", err, out)
	}
}
func TestRestoredBackupMustBeInvalidatedBeforeTraffic(t *testing.T) {
	s, c, ctx := fixture(t)
	a := newSession(ctx, t, s, 7)
	must(t, s.Create(ctx, a))
	keys := []string{s.authKey(a.SessionID), s.refreshKey(a.TokenHash), s.versionKey(7)}
	backup := map[string]string{}
	for _, k := range keys {
		v, err := c.Dump(ctx, k).Result()
		must(t, err)
		backup[k] = v
	}
	unrelated := s.prefix + "notebook:keep"
	must(t, c.Set(ctx, unrelated, "keep", 0).Err())
	must(t, s.Revoke(ctx, a.TokenHash, a.SessionClaims))
	invalid(t, s.ValidateAccess(ctx, a.SessionClaims))
	// Redis RESTORE использует RDB-сериализацию, возвращённую DUMP. Повтор старых
	// данных действительно может воскресить отозванную сессию до обязательной очистки.
	for _, k := range keys {
		must(t, c.Del(ctx, k).Err())
		must(t, c.Restore(ctx, k, time.Minute, backup[k]).Err())
	}
	must(t, s.ValidateAccess(ctx, a.SessionClaims))
	resetNamespace(t, s, c)
	invalid(t, s.ValidateAccess(ctx, a.SessionClaims))
	_, err := s.Find(ctx, a.TokenHash)
	invalid(t, err)
	for _, k := range keys {
		if c.Exists(ctx, k).Val() != 0 {
			t.Fatalf("restored key retained: %s", k)
		}
	}
	if c.Get(ctx, unrelated).Val() != "keep" {
		t.Fatal("reset erased unrelated data")
	}
	_, err = s.CurrentVersion(ctx, 7)
	must(t, err)
	invalid(t, s.ValidateAccess(ctx, a.SessionClaims))
}

// Нужен выделенный сервер: перезапуск общего интеграционного Redis помешал бы
// другим пакетам. CI использует Docker; локально можно передать бинарник.
func persistentServer(t *testing.T) (string, func() string, func()) {
	t.Helper()
	if os.Getenv("REDIS_RECOVERY_TESTS") != "1" {
		t.Skip("set REDIS_RECOVERY_TESTS=1 for real persistence restart")
	}
	if binary := os.Getenv("REDIS_SERVER_BIN"); binary != "" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		must(t, err)
		addr := listener.Addr().String()
		must(t, listener.Close())
		_, port, err := net.SplitHostPort(addr)
		must(t, err)
		dir := t.TempDir()
		var cmd *exec.Cmd
		start := func() {
			cmd = exec.Command(binary, "--bind", "127.0.0.1", "--port", port, "--dir", dir, "--save", "", "--appendonly", "yes", "--appendfsync", "everysec", "--requirepass", "test-redis-password")
			cmd.Stdout = nil
			cmd.Stderr = nil
			must(t, cmd.Start())
		}
		stop := func() {
			if cmd != nil {
				must(t, cmd.Process.Signal(syscall.SIGTERM))
				must(t, cmd.Wait())
				cmd = nil
			}
		}
		start()
		return addr, func() string { stop(); start(); return addr }, stop
	}
	name := "api17-redis-" + uuid.NewString()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v: %s", args[0], err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("volume", "create", name)
	t.Cleanup(func() { run("volume", "rm", name) })
	run("run", "-d", "--name", name, "-p", "127.0.0.1::6379", "-v", name+":/data", "redis:8-alpine", "redis-server", "--appendonly", "yes", "--appendfsync", "everysec", "--save", "", "--requirepass", "test-redis-password")
	addr := run("port", name, "6379/tcp")
	return addr, func() string {
		run("restart", name)
		return run("port", name, "6379/tcp")
	}, func() { run("rm", "-f", name) }
}
func waitRedis(t *testing.T, c *goredis.Client) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c.Ping(context.Background()).Err() == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("dedicated persistence Redis did not become ready")
}
func TestOrdinaryRedisRestartPreservesRevocationAndVersions(t *testing.T) {
	addr, restart, stop := persistentServer(t)
	t.Cleanup(stop)
	c := goredis.NewClient(&goredis.Options{Addr: addr, Password: "test-redis-password", MaxRetries: -1, DialTimeout: 100 * time.Millisecond})
	t.Cleanup(func() { _ = c.Close() })
	waitRedis(t, c)
	ctx := context.Background()
	s := NewStore(c, "test:"+uuid.NewString()+":", time.Second)
	a := newSession(ctx, t, s, 7)
	b := newSession(ctx, t, s, 8)
	must(t, s.Create(ctx, a))
	must(t, s.Create(ctx, b))
	must(t, s.RevokeAll(ctx, 7))
	before, err := s.CurrentVersion(ctx, 7)
	must(t, err)
	// После перезапуска Docker может назначить новый динамический порт хоста. Переподключаемся
	// по текущему адресу; состояние авторизации должно браться из сохранённого AOF.
	must(t, c.Close())
	addr = restart()
	c = goredis.NewClient(&goredis.Options{Addr: addr, Password: "test-redis-password", MaxRetries: -1, DialTimeout: 100 * time.Millisecond})
	waitRedis(t, c)
	s = NewStore(c, s.prefix, time.Second)
	after, err := s.CurrentVersion(ctx, 7)
	must(t, err)
	if before != after {
		t.Fatal("restart changed version")
	}
	invalid(t, s.ValidateAccess(ctx, a.SessionClaims))
	must(t, s.ValidateAccess(ctx, b.SessionClaims))
}
