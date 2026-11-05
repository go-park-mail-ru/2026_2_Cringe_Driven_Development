package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentclient "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/adapters/agent"
	U "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
)

// realSessionAgent запускает настоящий локальный Python-агент и Docker-kernel для проверки протокола без VPS
// Selectel.
type realSessionAgent struct {
	// t Контекст теста для проверок и освобождения ресурсов.
	t *testing.T
	// mu Блокировка совместного доступа к состоянию в памяти.
	mu sync.Mutex
	// language Язык тестового kernel.
	language func(context.Context, uuid.UUID) (string, error)
	// network Имя выделенной Docker-сети теста.
	network string
	// sessions Запущенные локальные агенты по UUID сессии.
	sessions map[uuid.UUID]*realAgentProcess
}

// realAgentProcess хранит защищённый Go-клиент и адрес одного локального тестового агента.
type realAgentProcess struct {
	// client Клиент внутреннего API агента.
	client *agentclient.Client
	// endpoint Адрес агента внутри доверенного соединения.
	endpoint U.AgentEndpoint
}

// newRealSessionAgent создаёт отдельную Docker-сеть и настоящий агент для локального интеграционного теста.
func newRealSessionAgent(t *testing.T, language func(context.Context, uuid.UUID) (string, error)) *realSessionAgent {
	t.Helper()
	network := "cellestial-api13-" + uuid.NewString()
	out, err := exec.Command("docker", "network", "create", "--driver", "bridge", network).CombinedOutput()
	if err != nil {
		t.Fatalf("create isolated kernel network: %v %s", err, out)
	}
	// Регистрируем очистку первой, чтобы сеть удалялась после остановки всех процессов.
	t.Cleanup(func() {
		out, err := exec.Command("docker", "network", "rm", network).CombinedOutput()
		if err != nil {
			t.Errorf("remove kernel test network: %v %s", err, out)
		}
	})
	return &realSessionAgent{t: t, language: language, network: network, sessions: map[uuid.UUID]*realAgentProcess{}}
}

// process находит агент сессии или запускает Python-сервис и защищённый локальный TLS-прокси.
func (a *realSessionAgent) process(ctx context.Context, id uuid.UUID) (*realAgentProcess, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p := a.sessions[id]; p != nil {
		return p, nil
	}
	language, err := a.language(ctx, id)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs("../../runtime-agent")
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	dir := a.t.TempDir()
	log, err := os.Create(filepath.Join(dir, "agent.log"))
	if err != nil {
		return nil, err
	}
	token := strings.Repeat("local-protocol-test-", 3)
	script := fmt.Sprintf(`from runtime_agent.api import create_app; import uvicorn; uvicorn.run(create_app(), host="127.0.0.1", port=%d, access_log=False, log_level="warning")`, port)
	cmd := exec.Command(filepath.Join(root, ".venv/bin/python"), "-c", script)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(root, "src"), "SESSION_ID="+id.String(), "KERNEL_LANGUAGE="+language, "RUNTIME_TOKEN="+token, "STATE_DIR="+filepath.Join(dir, "state"), "KERNEL_NETWORK="+a.network, "KERNEL_DNS=1.1.1.1")
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		_ = log.Close()
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	a.t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = log.Close()
	})
	address := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(35 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			return nil, errors.New("local agent did not listen within startup deadline")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case processErr := <-done:
			done <- processErr
			return nil, fmt.Errorf("local agent exited: %w", processErr)
		case <-time.After(100 * time.Millisecond):
		}
	}
	target, err := url.Parse("http://" + address)
	if err != nil {
		return nil, err
	}
	certPEM, keyPEM, cert, err := localAgentCertificate()
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	relay := httptest.NewUnstartedServer(httputil.NewSingleHostReverseProxy(target))
	relay.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
	relay.StartTLS()
	a.t.Cleanup(relay.Close)
	client, err := agentclient.New(certPEM, certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	p := &realAgentProcess{client: client, endpoint: U.AgentEndpoint{BaseURL: relay.URL, Token: token}}
	a.sessions[id] = p
	return p, nil
}

// localAgentCertificate создаёт временные сертификат и ключ для взаимной TLS-проверки только в тесте.
func localAgentCertificate() ([]byte, []byte, tls.Certificate, error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, tls.Certificate{}, err
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "local runtime test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		return nil, nil, tls.Certificate{}, err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	return certPEM, keyPEM, cert, err
}

// Health читает готовность агента, поколение kernel и состояние его очереди.
func (a *realSessionAgent) Health(ctx context.Context, id uuid.UUID) (D.Health, error) {
	p, err := a.process(ctx, id)
	if err != nil {
		return D.Health{}, err
	}
	h, status, err := p.client.Health(ctx, p.endpoint)
	if err != nil {
		return D.Health{}, err
	}
	if status != 200 && status != 503 {
		return D.Health{}, D.ErrUnavailable
	}
	sid, err := uuid.Parse(h.SessionID)
	if err != nil {
		return D.Health{}, err
	}
	return D.Health{SessionID: sid, Language: h.Language, State: h.State, Reason: h.Reason, Generation: h.Generation, Ready: h.Ready, Draining: h.Draining, Queued: h.Queued, ActiveExecutionID: h.ActiveExecutionID}, nil
}

// Execute передаёт сохранённую команду агенту; неизменный ID позволяет безопасно повторить сетевой запрос.
func (a *realSessionAgent) Execute(ctx context.Context, id uuid.UUID, c D.Command) (D.AgentExecution, error) {
	p, err := a.process(ctx, id)
	if err != nil {
		return D.AgentExecution{}, err
	}
	return p.client.ExecuteCommand(ctx, p.endpoint, c)
}

// Execution читает состояние и результат команды из внутреннего API агента.
func (a *realSessionAgent) Execution(ctx context.Context, id uuid.UUID, x string) (D.AgentExecution, error) {
	p, err := a.process(ctx, id)
	if err != nil {
		return D.AgentExecution{}, err
	}
	return p.client.ReadExecution(ctx, p.endpoint, x)
}

// Events читает очередную страницу внутреннего журнала агента после указанного seq.
func (a *realSessionAgent) Events(ctx context.Context, id uuid.UUID, after int64, limits D.EventLimits) ([]D.AgentEvent, error) {
	p, err := a.process(ctx, id)
	if err != nil {
		return nil, err
	}
	return p.client.ReadEvents(ctx, p.endpoint, after, limits)
}

// Activity передаёт агенту реальное действие пользователя, продлевающее время активности.
func (a *realSessionAgent) Activity(ctx context.Context, id uuid.UUID) error {
	p, err := a.process(ctx, id)
	if err != nil {
		return err
	}
	return p.client.Activity(ctx, p.endpoint)
}

// Drain запрещает новые команды у агента и запрашивает остановку выполняющейся команды перед завершением.
func (a *realSessionAgent) Drain(ctx context.Context, id uuid.UUID) error {
	p, err := a.process(ctx, id)
	if err != nil {
		return err
	}
	return p.client.Drain(ctx, p.endpoint)
}

// Stop просит агента остановить kernel, сохранив доступ к его журналу.
func (a *realSessionAgent) Stop(ctx context.Context, id uuid.UUID) error {
	p, err := a.process(ctx, id)
	if err != nil {
		return err
	}
	return p.client.Stop(ctx, p.endpoint)
}

// Interrupt запрашивает прерывание текущей команды без автоматического повтора при потере ответа.
func (a *realSessionAgent) Interrupt(ctx context.Context, id uuid.UUID) error {
	p, err := a.process(ctx, id)
	if err != nil {
		return err
	}
	return p.client.Interrupt(ctx, p.endpoint)
}

// Restart перезапускает свободный kernel и возвращает новое поколение; потерянный ответ автоматически не
// повторяется.
func (a *realSessionAgent) Restart(ctx context.Context, id uuid.UUID) (string, error) {
	p, err := a.process(ctx, id)
	if err != nil {
		return "", err
	}
	return p.client.Restart(ctx, p.endpoint)
}
