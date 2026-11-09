package fake

import (
	"context"
	"sync"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
)

// AgentPort — порт TLS-прокси агента на VPS.
const AgentPort = "8443"

// Host — in-memory usecase.HostConfigurator вместо Ansible.
//
// FailNext["Configure"] — ошибка следующего вызова (один раз). Block, если не nil, задерживает
// Configure до закрытия канала или отмены ctx. Agent, если задан, после успешной подготовки
// запускает агента в режиме ожидания по адресу https://<private_ip>:8443.
type Host struct {
	FailNext map[string]error
	Block    chan struct{}
	Agent    *Agent

	mu      sync.Mutex
	configs []usecase.HostConfig
}

// NewHost создаёт настройщик без задержек и ошибок.
func NewHost() *Host { return &Host{FailNext: map[string]error{}} }

// Fail потокобезопасно задаёт ошибку следующего вызова method.
func (h *Host) Fail(method string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.FailNext[method] = err
}

// Configure «готовит» VPS: учитывает вызов, ждёт Block и запускает в Agent агента в режиме ожидания.
func (h *Host) Configure(ctx context.Context, c usecase.HostConfig) error {
	h.mu.Lock()
	h.configs = append(h.configs, c)
	block := h.Block
	err := take(h.FailNext, "Configure")
	h.mu.Unlock()
	if err != nil {
		return err
	}
	if block != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-block:
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.Agent != nil {
		h.Agent.RegisterWaiting("https://" + c.PrivateIP + ":" + AgentPort)
	}
	return nil
}

// Calls возвращает число вызовов Configure.
func (h *Host) Calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.configs)
}

// Last возвращает параметры последнего вызова Configure.
func (h *Host) Last() usecase.HostConfig {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.configs) == 0 {
		return usecase.HostConfig{}
	}
	return h.configs[len(h.configs)-1]
}
