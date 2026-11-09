// Package domain содержит модель окружения (VPS) пользовательской сессии и правила переходов между состояниями.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// State — состояние окружения.
type State string

const (
	StateQueued         State = "QUEUED"
	StateCreating       State = "CREATING"
	StateConfiguring    State = "CONFIGURING"
	StateVerifying      State = "VERIFYING"
	StateReady          State = "READY"
	StateDeleting       State = "DELETING"
	StateDeleted        State = "DELETED"
	StateCleanupPending State = "CLEANUP_PENDING"
	StateFailed         State = "FAILED"
)

// Resources — идентификаторы облачных ресурсов окружения.
type Resources struct {
	ServerID  string
	VolumeID  string
	PortID    string
	PrivateIP string
}

// Environment — окружение пользовательской сессии.
type Environment struct {
	SessionID, OperationID   uuid.UUID
	Language, Profile        string
	State, DesiredState      State
	Resources                Resources
	Endpoint, AgentVersion   string
	LastError, CleanupResult string
	// PoolID VPS пула, из которой получено окружение; uuid.Nil — VPS создана для сессии.
	PoolID               uuid.UUID
	Version              int64
	CreatedAt, UpdatedAt time.Time
}

func transitions() map[State]map[State]struct{} {
	return map[State]map[State]struct{}{
		StateQueued:      {StateCreating: {}, StateVerifying: {}, StateDeleting: {}},
		StateCreating:    {StateConfiguring: {}, StateDeleting: {}, StateCleanupPending: {}},
		StateConfiguring: {StateVerifying: {}, StateDeleting: {}, StateCleanupPending: {}},
		// VERIFYING → CREATING: VPS пула не приняла назначение, окружение создаёт новую VPS.
		StateVerifying:      {StateReady: {}, StateCreating: {}, StateDeleting: {}, StateCleanupPending: {}},
		StateReady:          {StateDeleting: {}},
		StateDeleting:       {StateDeleted: {}, StateCleanupPending: {}},
		StateCleanupPending: {StateDeleting: {}, StateFailed: {}},
	}
}

// CanTransition сообщает, допустим ли переход from → to.
func CanTransition(from, to State) bool {
	_, ok := transitions()[from][to]
	return ok
}

// Finished сообщает, что состояние конечное.
func (s State) Finished() bool { return s == StateDeleted || s == StateFailed }

// ValidLanguage проверяет язык ядра: агент runtime-VPS поддерживает только python.
func ValidLanguage(l string) bool { return l == "python" }

// IsPoolVPS сообщает, что окружение описывает VPS пула без сессии (SessionID = PoolID): облачный
// адаптер помечает её ресурсы pool_id, а не session_id.
func (e Environment) IsPoolVPS() bool { return e.PoolID != uuid.Nil && e.PoolID == e.SessionID }
