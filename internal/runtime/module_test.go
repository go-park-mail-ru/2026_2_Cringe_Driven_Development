package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/config"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"github.com/google/uuid"
)

// TestRuntimeDisabledDoesNotStartWorker проверяет, что отключённый provisioning не запускает обработчик
// создания VPS.
func TestRuntimeDisabledDoesNotStartWorker(t *testing.T) {
	m, err := Build(context.Background(), config.RuntimeConfig{}, nil)
	svc, w := m.Environments, m.Worker
	if err != nil || w != nil || svc == nil {
		t.Fatalf("svc=%v worker=%v err=%v", svc, w, err)
	}
	if m.Agent == nil {
		t.Fatal("missing disabled session agent")
	}
	if _, err := m.Agent.Health(context.Background(), uuid.New()); err == nil {
		t.Fatal("disabled agent unexpectedly available")
	}
	if _, err := svc.RequestCreate(context.Background(), usecase.CreateEnvironment{SessionID: uuid.New(), Language: "python"}); !errors.Is(err, usecase.ErrDisabled) {
		t.Fatalf("err=%v", err)
	}
}

// TestRuntimeFakeBuilds проверяет сборку тестового окружения без настоящего облака.
func TestRuntimeFakeBuilds(t *testing.T) {
	cfg := config.RuntimeConfig{Enabled: true, Provider: "fake", SecretsKey: make([]byte, 32), MaxActive: 1, MaxConcurrentOps: 1}
	m, err := Build(context.Background(), cfg, nil)
	svc, w := m.Environments, m.Worker
	if err != nil || svc == nil || w == nil {
		t.Fatalf("svc=%v worker=%v err=%v", svc, w, err)
	}
}

func TestSmokeDepsUseOwnWorkSubdir(t *testing.T) {
	work := t.TempDir()
	// Каталог операции API (формат <session>-<n>) не должен пропасть из-за smoke.
	apiOp := filepath.Join(work, uuid.NewString()+"-1")
	if err := os.Mkdir(apiOp, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.RuntimeConfig{Enabled: true, Provider: "fake", SecretsKey: make([]byte, 32), MaxActive: 1, MaxConcurrentOps: 1, WorkDir: work}
	_, cleanup, err := BuildSmokeDeps(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	smokeDirs, _ := filepath.Glob(filepath.Join(work, "smoke-*"))
	if len(smokeDirs) != 1 {
		t.Fatalf("smoke dirs = %v", smokeDirs)
	}
	cleanup()
	if _, err := os.Stat(smokeDirs[0]); !os.IsNotExist(err) {
		t.Fatalf("smoke dir not removed: %v", err)
	}
	if _, err := os.Stat(apiOp); err != nil {
		t.Fatalf("API operation dir touched: %v", err)
	}
}

// TestRuntimeInitErrorOnBadCA проверяет отказ запуска runtime с некорректным доверенным сертификатом.
func TestRuntimeInitErrorOnBadCA(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(bad, []byte("not pem SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.RuntimeConfig{
		Enabled: true, Provider: "openstack", SecretsKey: make([]byte, 32),
		TLSCACertFile: bad, TLSCAKeyFile: bad, TLSClientCertFile: bad, TLSClientKeyFile: bad,
	}
	if m, err := Build(context.Background(), cfg, nil); err == nil || m.Worker != nil {
		w := m.Worker
		t.Fatalf("expected error, got worker=%v err=%v", w, err)
	}
}
