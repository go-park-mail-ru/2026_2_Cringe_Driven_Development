// Package runtime собирает модуль создания VPS пользовательских сессий.
package runtime

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/config"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/adapters/agent"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/adapters/ansible"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/adapters/fake"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/adapters/openstack"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/adapters/pki"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/adapters/postgres"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/adapters/sessionagent"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/smoke"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	sessionusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/usecase"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	workerLease    = 60 * time.Second
	workerPoll     = 2 * time.Second
	fakeCloudDelay = 200 * time.Millisecond
	artifactsEnv   = "RUNTIME_ARTIFACTS_DIR"
	artifactsBase  = "/app"
)

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// parts — собранные адаптеры и общие настройки; из них строятся воркеры Build и smoke.
type parts struct {
	cfg   config.RuntimeConfig
	repo  *postgres.Repository
	cloud usecase.CloudProvider
	host  usecase.HostConfigurator
	ag    usecase.AgentClient
	exec  smoke.Executor
	sec   usecase.Secrets
}

// worker создаёт воркер с заданными владельцем и lease.
func (p parts) worker(owner string, lease time.Duration) *usecase.Worker {
	cfg := p.cfg
	return usecase.NewWorker(p.repo, p.repo, p.repo, p.cloud, p.host, p.ag, p.sec, systemClock{}, usecase.WorkerSettings{
		Owner:       owner,
		Concurrency: cfg.MaxConcurrentOps, Lease: lease, PollInterval: workerPoll,
		ReconcileInterval: cfg.ReconcileInterval, StartupTimeout: cfg.StartupTimeout,
		AnsibleTimeout: cfg.AnsibleTimeout, DrainTimeout: cfg.DrainTimeout, DeleteTimeout: cfg.DeleteTimeout,
		PythonImage: cfg.PythonImage, KernelDNS: cfg.KernelDNS,
		BlockedPublicIPs: cfg.BlockedPublicIPs, EnvName: cfg.EnvName,
		PoolSize: cfg.WarmPoolSize, ImageID: cfg.ImageID, AgentVersion: cfg.AgentVersion,
	})
}

func newService(repo *postgres.Repository, cfg config.RuntimeConfig) *usecase.Service {
	return usecase.NewService(repo, usecase.Settings{
		Enabled: true, MaxActive: cfg.MaxActive, AgentVersion: cfg.AgentVersion, Profiles: map[string]bool{"default": true},
	})
}

// assemble создаёт адаптеры по cfg.Provider; cfg.Enabled должен быть true.
func assemble(ctx context.Context, cfg config.RuntimeConfig, pool *pgxpool.Pool) (parts, error) {
	p := parts{cfg: cfg, repo: postgres.NewRepository(pool)}
	clock := systemClock{}
	switch cfg.Provider {
	case "fake":
		caCert, caKey, err := throwawayCA()
		if err != nil {
			return parts{}, fmt.Errorf("runtime: fake CA: %w", err)
		}
		if p.sec, err = pki.New(cfg.SecretsKey, caCert, caKey, clock); err != nil {
			return parts{}, fmt.Errorf("runtime: secrets: %w", err)
		}
		c, h, a := fake.New(fakeCloudDelay)
		p.cloud, p.host, p.ag, p.exec = c, h, a, a
	case "openstack":
		osa, err := buildOpenStack(ctx, cfg, clock)
		if err != nil {
			return parts{}, err
		}
		p.cloud, p.host, p.ag, p.exec, p.sec = osa.cloud, osa.host, osa.agent, osa.agent, osa.secrets
	default:
		return parts{}, fmt.Errorf("runtime: unknown provider %q", cfg.Provider)
	}
	return p, nil
}

func ownerName() string {
	name, _ := os.Hostname()
	var suffix [4]byte
	_, _ = rand.Read(suffix[:])
	return fmt.Sprintf("%s-%x", name, suffix)
}

// Module использует одни и те же адаптеры и секреты для provisioning и сессий.
type Module struct {
	// Environments Сервис создания, чтения и удаления окружений.
	Environments usecase.EnvironmentService
	// Worker Фоновый обработчик сервиса окружений.
	Worker *usecase.Worker
	// Agent Внутренний доступ к агенту подготовленного окружения.
	Agent sessionusecase.SessionAgent
}

// Build не создаёт облачные адаптеры, если runtime выключен.
func Build(ctx context.Context, cfg config.RuntimeConfig, pool *pgxpool.Pool) (Module, error) {
	if !cfg.Enabled {
		return Module{Environments: usecase.NewService(postgres.NewRepository(pool), usecase.Settings{Enabled: false, Profiles: map[string]bool{"default": true}}), Agent: sessionagent.New(nil, nil, nil)}, nil
	}
	p, err := assemble(ctx, cfg, pool)
	if err != nil {
		return Module{}, err
	}
	client, ok := p.ag.(sessionagent.Client)
	if !ok {
		return Module{}, errors.New("runtime: session agent client unavailable")
	}
	return Module{Environments: newService(p.repo, cfg), Worker: p.worker(ownerName(), workerLease), Agent: sessionagent.New(p.repo, p.sec, client)}, nil
}

// BuildSmokeDeps собирает зависимости runtime-smoke (cfg.Enabled должен быть true); cleanup удаляет
// рабочий подкаталог smoke.
func BuildSmokeDeps(ctx context.Context, cfg config.RuntimeConfig, pool *pgxpool.Pool) (smoke.Deps, func(), error) {
	if !cfg.Enabled {
		return smoke.Deps{}, nil, usecase.ErrDisabled
	}
	// Свой подкаталог RUNTIME_WORK_DIR/smoke-<random>: ansible.New чистит устаревшие каталоги
	// операций в рабочем каталоге и не должен задеть каталоги работающего API.
	if err := os.MkdirAll(cfg.WorkDir, 0o700); err != nil {
		return smoke.Deps{}, nil, fmt.Errorf("runtime: create work dir: %w", err)
	}
	dir, err := os.MkdirTemp(cfg.WorkDir, "smoke-")
	if err != nil {
		return smoke.Deps{}, nil, fmt.Errorf("runtime: create smoke work dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	cfg.WorkDir = dir
	p, err := assemble(ctx, cfg, pool)
	if err != nil {
		cleanup()
		return smoke.Deps{}, nil, err
	}
	return smoke.Deps{
		Service: newService(p.repo, cfg), Repo: p.repo, Secrets: p.sec, Agent: p.exec, Cloud: p.cloud,
		NewWorker: p.worker,
	}, cleanup, nil
}

// openStackAdapters — конкретные адаптеры боевого режима.
type openStackAdapters struct {
	cloud   *openstack.Provider
	host    *ansible.Runner
	agent   *agent.Client
	secrets *pki.Secrets
}

func buildOpenStack(ctx context.Context, cfg config.RuntimeConfig, clock usecase.Clock) (openStackAdapters, error) {
	read := func(path string) ([]byte, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("runtime: read %s: %w", filepath.Base(path), err)
		}
		return b, nil
	}
	caCert, err := read(cfg.TLSCACertFile)
	if err != nil {
		return openStackAdapters{}, err
	}
	caKey, err := read(cfg.TLSCAKeyFile)
	if err != nil {
		return openStackAdapters{}, err
	}
	clientCert, err := read(cfg.TLSClientCertFile)
	if err != nil {
		return openStackAdapters{}, err
	}
	clientKey, err := read(cfg.TLSClientKeyFile)
	if err != nil {
		return openStackAdapters{}, err
	}
	sec, err := pki.New(cfg.SecretsKey, caCert, caKey, clock)
	if err != nil {
		return openStackAdapters{}, fmt.Errorf("runtime: secrets: %w", err)
	}
	ag, err := agent.New(caCert, clientCert, clientKey)
	if err != nil {
		return openStackAdapters{}, fmt.Errorf("runtime: agent client: %w", err)
	}
	art, playbook, err := artifacts(cfg.TLSCACertFile)
	if err != nil {
		return openStackAdapters{}, err
	}
	host, err := ansible.New(cfg.WorkDir, playbook, cfg.SSHPrivateKeyFile, cfg.SSHUser, "ansible-playbook", art)
	if err != nil {
		return openStackAdapters{}, fmt.Errorf("runtime: ansible: %w", err)
	}
	cloud, err := openstack.New(ctx, cfg)
	if err != nil {
		return openStackAdapters{}, fmt.Errorf("runtime: openstack: %w", err)
	}
	return openStackAdapters{cloud: cloud, host: host, agent: ag, secrets: sec}, nil
}

// artifacts находит артефакты агента и плейбук (ruling R8).
func artifacts(caCertFile string) (ansible.Artifacts, string, error) {
	base := os.Getenv(artifactsEnv)
	if base == "" {
		base = artifactsBase
	}
	base, err := filepath.Abs(base)
	if err != nil {
		return ansible.Artifacts{}, "", fmt.Errorf("runtime: artifacts dir: %w", err)
	}
	caAbs, err := filepath.Abs(caCertFile)
	if err != nil {
		return ansible.Artifacts{}, "", fmt.Errorf("runtime: CA cert path: %w", err)
	}
	agentDir := filepath.Join(base, "agent")
	wheels, err := filepath.Glob(filepath.Join(agentDir, "cellestial_agent-*.whl"))
	if err != nil || len(wheels) != 1 {
		return ansible.Artifacts{}, "", fmt.Errorf("runtime: expected exactly one cellestial_agent-*.whl in %s, found %d", agentDir, len(wheels))
	}
	art := ansible.Artifacts{
		AgentWheel:           wheels[0],
		AgentWheelSHA256File: wheels[0] + ".sha256",
		AgentRequirements:    filepath.Join(agentDir, "requirements.txt"),
		AgentService:         filepath.Join(agentDir, "cellestial-agent.service"),
		CACertFile:           caAbs,
	}
	for _, p := range []string{art.AgentWheelSHA256File, art.AgentRequirements, art.AgentService} {
		if _, err := os.Stat(p); err != nil {
			return ansible.Artifacts{}, "", fmt.Errorf("runtime: artifact %s: %w", filepath.Base(p), err)
		}
	}
	return art, filepath.Join(base, "deploy", "runtime", "prepare.yml"), nil
}

// throwawayCA создаёт одноразовый CA в памяти для RUNTIME_PROVIDER=fake.
func throwawayCA() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "cellestial-fake-ca"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	kd, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}), nil
}
