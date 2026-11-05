// Package agent — HTTPS-клиент runtime-agent (mTLS + Bearer).
package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
)

const (
	// maxBody ограничивает размер ответа агента; остальные константы ограничивают установку и ожидание
	// соединения.
	maxBody = 64 << 20
	// dialTimeout и tlsTimeout ограничивают установку соединения с VPS.
	dialTimeout = 10 * time.Second
	tlsTimeout  = 10 * time.Second
	idleTimeout = 30 * time.Second
	// defaultHealthTimeout — лимит одного вызова /health: зависший запрос не должен откладывать
	// проверку StartupTimeout воркером.
	defaultHealthTimeout = 10 * time.Second
)

// Client реализует usecase.AgentClient поверх HTTPS с mTLS.
type Client struct {
	http          *http.Client
	healthTimeout time.Duration
}

var _ usecase.AgentClient = (*Client)(nil)

// New создаёт клиента: доверяет только caPEM, предъявляет клиентский сертификат.
func New(caPEM, clientCertPEM, clientKeyPEM []byte) (*Client, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("agent client: invalid CA PEM")
	}
	cert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("agent client: client certificate: %w", err)
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("agent client: unexpected default transport")
	}
	// Клон DefaultTransport: агенты в приватной сети, прокси из окружения не используется.
	tr := base.Clone()
	tr.Proxy = nil
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, Certificates: []tls.Certificate{cert}}
	tr.TLSHandshakeTimeout = tlsTimeout
	tr.DialContext = (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext
	tr.IdleConnTimeout = idleTimeout
	return &Client{http: &http.Client{Transport: tr, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, healthTimeout: defaultHealthTimeout}, nil
}

// do выполняет запрос; ошибки не содержат токена.
func (c *Client) do(ctx context.Context, ep usecase.AgentEndpoint, method, path string, body any) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var rd io.Reader
	if body != nil {
		var buf bytes.Buffer
		encoder := json.NewEncoder(&buf)
		encoder.SetEscapeHTML(false)
		err := encoder.Encode(body)
		b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, ep.BaseURL+path, rd)
	if err != nil {
		return 0, nil, fmt.Errorf("agent %s %s: bad request", method, path)
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, nil, fmt.Errorf("agent %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return 0, nil, fmt.Errorf("agent %s %s: read body: %w", method, path, err)
	}
	if len(data) > maxBody {
		return 0, nil, errors.New("agent response exceeds limit")
	}
	return resp.StatusCode, data, nil
}

func statusErr(method, path string, status int) error {
	return fmt.Errorf("agent %s %s: HTTP %d", method, path, status)
}

// Health возвращает /health; 503 — не ошибка (тело разбирается), прочие статусы ≠200 возвращаются без разбора.
// Вызов ограничен healthTimeout.
func (c *Client) Health(ctx context.Context, ep usecase.AgentEndpoint) (usecase.AgentHealth, int, error) {
	ctx, cancel := context.WithTimeout(ctx, c.healthTimeout)
	defer cancel()
	status, data, err := c.do(ctx, ep, http.MethodGet, "/health", nil)
	if err != nil {
		return usecase.AgentHealth{}, 0, err
	}
	if status != http.StatusOK && status != http.StatusServiceUnavailable {
		return usecase.AgentHealth{}, status, nil
	}
	var v struct {
		SessionID         string  `json:"session_id"`
		Language          string  `json:"language"`
		State             string  `json:"state"`
		Ready             bool    `json:"ready"`
		Reason            string  `json:"reason"`
		Generation        string  `json:"generation"`
		ActiveExecutionID *string `json:"active_execution_id"`
		Queued            int     `json:"queued"`
		Draining          bool    `json:"draining"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return usecase.AgentHealth{}, status, fmt.Errorf("agent GET /health: decode: %w", err)
	}
	return usecase.AgentHealth{SessionID: v.SessionID, Language: v.Language, State: v.State, Ready: v.Ready, Reason: v.Reason, Generation: v.Generation, ActiveExecutionID: v.ActiveExecutionID, Queued: v.Queued, Draining: v.Draining}, status, nil
}

func (c *Client) post(ctx context.Context, ep usecase.AgentEndpoint, path string, want int) error {
	status, _, err := c.do(ctx, ep, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	if status != want {
		return statusErr(http.MethodPost, path, status)
	}
	return nil
}

// Drain переводит агента в DRAINING.
func (c *Client) Drain(ctx context.Context, ep usecase.AgentEndpoint) error {
	return c.post(ctx, ep, "/runtime/drain", http.StatusOK)
}

// Stop останавливает сессию; 503 (очистка не завершена) — ошибка, вызов можно повторить.
func (c *Client) Stop(ctx context.Context, ep usecase.AgentEndpoint) error {
	return c.post(ctx, ep, "/runtime/stop", http.StatusOK)
}

// Execute отправляет код на выполнение (идемпотентно по executionID).
func (c *Client) Execute(ctx context.Context, ep usecase.AgentEndpoint, executionID, code string) error {
	status, _, err := c.do(ctx, ep, http.MethodPost, "/executions", map[string]string{"execution_id": executionID, "code": code})
	if err != nil {
		return err
	}
	if status != http.StatusAccepted {
		return statusErr(http.MethodPost, "/executions", status)
	}
	return nil
}

// Execution возвращает состояние выполнения.
func (c *Client) Execution(ctx context.Context, ep usecase.AgentEndpoint, id string) (string, error) {
	path := "/executions/" + url.PathEscape(id)
	status, data, err := c.do(ctx, ep, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", statusErr(http.MethodGet, "/executions", status)
	}
	var v struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return "", fmt.Errorf("agent GET /executions: decode: %w", err)
	}
	return v.State, nil
}

// Events возвращает страницу событий с seq > after.
func (c *Client) Events(ctx context.Context, ep usecase.AgentEndpoint, after int) ([]json.RawMessage, error) {
	path := fmt.Sprintf("/events?after=%d", after)
	status, data, err := c.do(ctx, ep, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, statusErr(http.MethodGet, "/events", status)
	}
	var v struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("agent GET /events: decode: %w", err)
	}
	if v.Events == nil {
		v.Events = []json.RawMessage{}
	}
	return v.Events, nil
}
