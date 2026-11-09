package ansible

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type env struct {
	home, work, pbDir string
	runner            *Runner
}

func setup(t *testing.T) env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := filepath.Join(t.TempDir(), "work")
	art := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(art, name)
		if gotErr := os.WriteFile(p, []byte(s), 0o600); gotErr != nil {
			t.Fatal(gotErr)
		}
		return p
	}
	bin, err := filepath.Abs("testdata/fake-ansible-playbook")
	if err != nil {
		t.Fatal(err)
	}
	pbDir := t.TempDir()
	r, err := New(work, filepath.Join(pbDir, "playbook.yml"), "/keys/id", "ubuntu", bin, Artifacts{
		AgentWheel:           "/a/agent.whl",
		AgentWheelSHA256File: write("sha", "abc123\n"),
		AgentRequirements:    "/a/req.txt",
		AgentService:         "/a/agent.service",
		CACertFile:           write("ca.pem", "CA-PEM"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return env{home: home, work: work, pbDir: pbDir, runner: r}
}

func cfg() usecase.HostConfig {
	return usecase.HostConfig{
		Name: uuid.New(), PrivateIP: "10.0.0.5", HostPublicKey: "ssh-ed25519 AAAATESTKEY",
		ServerCertPEM: []byte("CERT"), ServerKeyPEM: []byte("KEY"),
		PythonImage: "py:1", KernelDNS: "1.1.1.1",
		BlockedPublicIPs: []string{"1.2.3.4"},
	}
}

func readHome(t *testing.T, e env, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.home, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestRunnerWritesFilesWithModes(t *testing.T) {
	e := setup(t)
	if gotErr := e.runner.Configure(context.Background(), cfg()); gotErr != nil {
		t.Fatal(gotErr)
	}
	if diff := cmp.Diff("dir=700 vars=600", readHome(t, e, "modes.txt")); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("10.0.0.5 ssh-ed25519 AAAATESTKEY", readHome(t, e, "known_hosts.txt")); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

func TestRunnerPassesNoSecretsInArgs(t *testing.T) {
	e := setup(t)
	if gotErr := e.runner.Configure(context.Background(), cfg()); gotErr != nil {
		t.Fatal(gotErr)
	}
	args := readHome(t, e, "args.txt")
	if strings.Contains(args, "KEY") {
		t.Fatalf("%q contains %q", args, "KEY")
	}
	if !strings.Contains(args, "playbook.yml") {
		t.Fatalf("%q does not contain %q", args, "playbook.yml")
	}
}

func TestRunnerKillsProcessGroupOnCancel(t *testing.T) {
	e := setup(t)
	if gotErr := os.WriteFile(filepath.Join(e.home, "sleep"), nil, 0o600); gotErr != nil {
		t.Fatal(gotErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.runner.Configure(ctx, cfg()) }()
	pidFile := filepath.Join(e.home, "sleep.pid")
	waitFor(t, func() bool { _, err := os.Stat(pidFile); return err == nil })
	time.Sleep(100 * time.Millisecond)
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Configure did not return")
	}
	waitFor(t, func() bool { return syscall.Kill(pid, 0) != nil })
}

func TestRunnerRemovesWorkDir(t *testing.T) {
	e := setup(t)
	if gotErr := e.runner.Configure(context.Background(), cfg()); gotErr != nil {
		t.Fatal(gotErr)
	}
	dir := readHome(t, e, "dir.txt")
	_, err := os.Stat(dir)
	if !os.IsNotExist(err) {
		t.Fatal("expected os.IsNotExist(err)")
	}
}

func TestRunnerVarsContent(t *testing.T) {
	e := setup(t)
	c := cfg()
	vars, err := e.runner.vars(c)
	if err != nil {
		t.Fatal(err)
	}
	// Полный набор ключей: лишний ключ (например, host key) или пропавший — ошибка.
	if diff := cmp.Diff(map[string]any{
		"private_ip":         "10.0.0.5",
		"server_cert_pem":    "CERT",
		"server_key_pem":     "KEY",
		"python_image":       "py:1",
		"kernel_dns":         "1.1.1.1",
		"blocked_public_ips": []string{"1.2.3.4"},
		"agent_wheel":        "/a/agent.whl",
		"agent_wheel_sha256": "abc123",
		"agent_requirements": "/a/req.txt",
		"agent_service":      "/a/agent.service",
		"ca_cert_pem":        "CA-PEM",
	}, vars); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

func TestRunnerSetsUTF8Locale(t *testing.T) {
	e := setup(t)
	t.Setenv("LC_ALL", "POSIX")
	t.Setenv("LANG", "")
	if gotErr := e.runner.Configure(context.Background(), cfg()); gotErr != nil {
		t.Fatal(gotErr)
	}
	if diff := cmp.Diff("LC_ALL=C.UTF-8 LANG=C.UTF-8", readHome(t, e, "locale.txt")); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

func TestRunnerRunsInPlaybookDir(t *testing.T) {
	e := setup(t)
	if gotErr := e.runner.Configure(context.Background(), cfg()); gotErr != nil {
		t.Fatal(gotErr)
	}
	want, err := filepath.EvalSymlinks(e.pbDir)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, readHome(t, e, "cwd.txt")); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

func TestNewRemovesStaleOperationDirs(t *testing.T) {
	work := filepath.Join(t.TempDir(), "work")
	if gotErr := os.MkdirAll(work, 0o700); gotErr != nil {
		t.Fatal(gotErr)
	}
	stale := filepath.Join(work, uuid.NewString()+"-123456")
	foreignDir := filepath.Join(work, "keep-me")
	foreignFile := filepath.Join(work, uuid.NewString()+"-999.txt")
	if gotErr := os.MkdirAll(stale, 0o700); gotErr != nil {
		t.Fatal(gotErr)
	}
	if gotErr := os.WriteFile(filepath.Join(stale, "vars.json"), []byte("{}"), 0o600); gotErr != nil {
		t.Fatal(gotErr)
	}
	if gotErr := os.MkdirAll(foreignDir, 0o700); gotErr != nil {
		t.Fatal(gotErr)
	}
	if gotErr := os.WriteFile(foreignFile, nil, 0o600); gotErr != nil {
		t.Fatal(gotErr)
	}
	_, err := New(work, "pb.yml", "k", "u", "bin", Artifacts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale dir still exists: %v", err)
	}
	for _, p := range []string{work, foreignDir, foreignFile} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s removed: %v", p, err)
		}
	}
}

func TestNewFailsWhenWorkDirUncreatable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if gotErr := os.WriteFile(file, nil, 0o600); gotErr != nil {
		t.Fatal(gotErr)
	}
	_, err := New(filepath.Join(file, "sub"), "pb.yml", "k", "u", "bin", Artifacts{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNewFailsWhenWorkDirLocked(t *testing.T) {
	dir := t.TempDir()
	_, err := New(dir, "prepare.yml", "key", "root", "ansible-playbook", Artifacts{})
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "00000000-0000-0000-0000-000000000000-1")
	if gotErr := os.Mkdir(stale, 0o700); gotErr != nil {
		t.Fatal(gotErr)
	}
	_, err = New(dir, "prepare.yml", "key", "root", "ansible-playbook", Artifacts{})
	if err == nil {
		t.Fatal("expected error")
	}
	if _, statErr := os.Stat(stale); statErr != nil {
		t.Fatalf("занятый каталог нельзя чистить: %v", statErr)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("condition not met")
}
