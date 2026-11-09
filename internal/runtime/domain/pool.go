package domain

import (
	"time"

	"github.com/google/uuid"
)

// PoolState — состояние VPS пула.
type PoolState string

const (
	PoolPreparing PoolState = "PREPARING"
	PoolReady     PoolState = "READY"
	PoolClaimed   PoolState = "CLAIMED"
	PoolDeleting  PoolState = "DELETING"
)

// PoolVPS — заранее подготовленная VPS без сессии: агент запущен в режиме ожидания.
type PoolVPS struct {
	ID        uuid.UUID
	State     PoolState
	Resources Resources
	// ImageID и AgentVersion — RUNTIME_IMAGE_ID и RUNTIME_AGENT_VERSION, на которых VPS подготовлена.
	ImageID, AgentVersion string
	// HostPrivateKey — запечатанный host key, HostPublicKey — его открытая часть.
	HostPrivateKey []byte
	HostPublicKey  string
	// SessionID — сессия, захватившая VPS (CLAIMED).
	SessionID            uuid.UUID
	LastError            string
	CreatedAt, UpdatedAt time.Time
}

// Env — окружение-носитель VPS пула для облачного адаптера (см. Environment.IsPoolVPS).
func (p PoolVPS) Env() Environment {
	return Environment{SessionID: p.ID, OperationID: p.ID, PoolID: p.ID, Resources: p.Resources}
}
