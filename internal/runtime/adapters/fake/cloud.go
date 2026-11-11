// Package fake содержит in-memory реализации облака, настройки VPS и агента для
// RUNTIME_PROVIDER=fake и тестов воркера.
package fake

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"github.com/google/uuid"
)

// New создаёт связанные облако, настройщик и агента: после Host.Configure агент по адресу
// https://<private_ip>:8443 ждёт назначения (POST /assign).
func New(delay time.Duration) (*Cloud, *Host, *Agent) {
	agent := NewAgent()
	host := NewHost()
	host.Agent = agent
	return NewCloud(delay), host, agent
}

type resource struct {
	kind, id, ip string
	session      uuid.UUID // сессия или VPS пула (pool)
	pool         bool
}

// Cloud — in-memory usecase.CloudProvider. Каждый вызов ждёт Delay (с учётом ctx).
//
// FailNext[метод] — ошибка, которую вернёт следующий вызов метода до каких-либо изменений;
// FailAfter[метод] — ошибка после успешного выполнения (имитация потерянного ответа POST).
// Обе ошибки срабатывают один раз. Карты можно заполнять до запуска воркера; во время работы —
// через Fail и FailAfterOnce.
type Cloud struct {
	FailNext  map[string]error
	FailAfter map[string]error

	mu        sync.Mutex
	delay     time.Duration
	resources map[string]*resource // id → ресурс
	calls     map[string]int
	seq       int
}

// NewCloud создаёт пустое облако с задержкой delay на каждый вызов.
func NewCloud(delay time.Duration) *Cloud {
	return &Cloud{
		FailNext:  map[string]error{},
		FailAfter: map[string]error{},
		delay:     delay,
		resources: map[string]*resource{},
		calls:     map[string]int{},
	}
}

// Fail потокобезопасно задаёт ошибку следующего вызова method.
func (c *Cloud) Fail(method string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.FailNext[method] = err
}

// FailAfterOnce потокобезопасно задаёт ошибку после успешного выполнения следующего вызова method.
func (c *Cloud) FailAfterOnce(method string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.FailAfter[method] = err
}

// Calls возвращает число вызовов метода.
func (c *Cloud) Calls(method string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[method]
}

// Count возвращает число существующих ресурсов вида kind: server, volume, port.
func (c *Cloud) Count(kind string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.resources {
		if r.kind == kind {
			n++
		}
	}
	return n
}

// Add создаёт ресурс сессии в обход Ensure* (например, сироту для проверки recovery) и возвращает ID.
func (c *Cloud) Add(kind string, session uuid.UUID) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.create(kind, session, false).id
}

// AddPool создаёт ресурс VPS пула (метка pool_id) в обход Ensure* и возвращает ID.
func (c *Cloud) AddPool(kind string, pool uuid.UUID) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.create(kind, pool, true).id
}

// begin учитывает вызов, ждёт задержку и возвращает ошибку из FailNext.
func (c *Cloud) begin(ctx context.Context, method string) error {
	c.mu.Lock()
	c.calls[method]++
	delay := c.delay
	c.mu.Unlock()
	if delay > 0 {
		t := time.NewTimer(delay)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return take(c.FailNext, method)
}

func (c *Cloud) after(method string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return take(c.FailAfter, method)
}

func take(m map[string]error, key string) error {
	err, ok := m[key]
	if ok {
		delete(m, key)
	}
	return err
}

// create добавляет ресурс; вызывается под c.mu.
func (c *Cloud) create(kind string, session uuid.UUID, pool bool) *resource {
	c.seq++
	r := &resource{kind: kind, id: fmt.Sprintf("%s-%d", kind, c.seq), session: session, pool: pool}
	if kind == "port" {
		r.ip = fmt.Sprintf("10.0.%d.%d", c.seq/250, c.seq%250+2)
	}
	c.resources[r.id] = r
	return r
}

// ensure находит ресурс по known или владельцу окружения, иначе создаёт; вызывается под c.mu.
func (c *Cloud) ensure(kind, known string, env domain.Environment) *resource {
	if r, ok := c.resources[known]; ok && r.kind == kind {
		return r
	}
	for _, r := range c.sortedLocked() {
		if r.kind == kind && r.session == env.SessionID && r.pool == env.IsPoolVPS() {
			return r
		}
	}
	return c.create(kind, env.SessionID, env.IsPoolVPS())
}

func (c *Cloud) sortedLocked() []*resource {
	out := make([]*resource, 0, len(c.resources))
	for _, r := range c.resources {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// EnsurePort находит или создаёт порт сессии с приватным IP.
func (c *Cloud) EnsurePort(ctx context.Context, env domain.Environment) (string, string, error) {
	if err := c.begin(ctx, "EnsurePort"); err != nil {
		return "", "", err
	}
	c.mu.Lock()
	r := c.ensure("port", env.Resources.PortID, env)
	c.mu.Unlock()
	return r.id, r.ip, c.after("EnsurePort")
}

// EnsureVolume находит или создаёт загрузочный том сессии.
func (c *Cloud) EnsureVolume(ctx context.Context, env domain.Environment) (string, error) {
	if err := c.begin(ctx, "EnsureVolume"); err != nil {
		return "", err
	}
	c.mu.Lock()
	r := c.ensure("volume", env.Resources.VolumeID, env)
	c.mu.Unlock()
	return r.id, c.after("EnsureVolume")
}

// EnsureServer находит или создаёт VPS сессии; требует порт и том.
func (c *Cloud) EnsureServer(ctx context.Context, env domain.Environment, _ string) (string, error) {
	if err := c.begin(ctx, "EnsureServer"); err != nil {
		return "", err
	}
	if env.Resources.PortID == "" || env.Resources.VolumeID == "" {
		return "", fmt.Errorf("fake: server needs port and volume: %w", usecase.ErrCloudPermanent)
	}
	c.mu.Lock()
	r := c.ensure("server", env.Resources.ServerID, env)
	c.mu.Unlock()
	return r.id, c.after("EnsureServer")
}

func (c *Cloud) remove(ctx context.Context, method, kind, id string) error {
	if err := c.begin(ctx, method); err != nil {
		return err
	}
	c.mu.Lock()
	if r, ok := c.resources[id]; ok && r.kind == kind {
		delete(c.resources, id)
	}
	c.mu.Unlock()
	return c.after(method)
}

// DeleteServer удаляет VPS; отсутствующая — успех.
func (c *Cloud) DeleteServer(ctx context.Context, id string) error {
	return c.remove(ctx, "DeleteServer", "server", id)
}

// DeleteVolume удаляет том; отсутствующий — успех.
func (c *Cloud) DeleteVolume(ctx context.Context, id string) error {
	return c.remove(ctx, "DeleteVolume", "volume", id)
}

// DeletePort удаляет порт; отсутствующий — успех.
func (c *Cloud) DeletePort(ctx context.Context, id string) error {
	return c.remove(ctx, "DeletePort", "port", id)
}

// ListOwned возвращает все ресурсы, упорядоченные по ID.
func (c *Cloud) ListOwned(ctx context.Context) ([]usecase.CloudResource, error) {
	if err := c.begin(ctx, "ListOwned"); err != nil {
		return nil, err
	}
	c.mu.Lock()
	var out []usecase.CloudResource
	for _, r := range c.sortedLocked() {
		res := usecase.CloudResource{Kind: r.kind, ID: r.id, SessionID: r.session}
		if r.pool {
			res.SessionID, res.PoolID = uuid.Nil, r.session
		}
		out = append(out, res)
	}
	c.mu.Unlock()
	return out, c.after("ListOwned")
}
