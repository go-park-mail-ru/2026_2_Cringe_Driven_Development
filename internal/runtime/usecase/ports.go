package usecase

import (
	"context"
	"errors"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/google/uuid"
)

var (
	// ErrStale — оптимистическая блокировка или владение заданием потеряны.
	ErrStale = errors.New("environment version is stale or job is not owned")
	// ErrLeaseLost — lease задания перехвачен другим воркером.
	ErrLeaseLost = errors.New("job lease lost")
	// ErrCloudTransient — временная ошибка облака, можно повторить.
	ErrCloudTransient = errors.New("cloud transient error")
	// ErrCloudPermanent — постоянная ошибка облака (квота, 403), повтор бесполезен.
	ErrCloudPermanent = errors.New("cloud permanent error")
)

// JobKind — вид задания.
type JobKind string

const (
	JobCreate JobKind = "create"
	JobDelete JobKind = "delete"
)

// Job — задание воркера.
type Job struct {
	ID        int64
	SessionID uuid.UUID
	Kind      JobKind
	Attempt   int
}

// SealedSecrets — зашифрованные секреты окружения.
type SealedSecrets struct {
	Token, HostPrivateKey []byte
	HostPublicKey         string
}

// EnvironmentRepository хранит окружения и задания.
type EnvironmentRepository interface {
	// CreateWithJob атомарно создаёт окружение и задание create. Существующий session_id → (его, false, nil).
	// Активных (не Finished) ≥ maxActive → ErrCapacity.
	CreateWithJob(ctx context.Context, env domain.Environment, maxActive int) (domain.Environment, bool, error)
	Get(ctx context.Context, id uuid.UUID) (domain.Environment, error) // ErrNotFound
	// MarkForDelete ставит desired_state=DELETED; активное задание сессии переводит в kind=delete, иначе вставляет delete.
	// Для Finished — no-op. Нет окружения → ErrNotFound.
	MarkForDelete(ctx context.Context, id uuid.UUID, reason string) error
	// Save: UPDATE … WHERE version=env.Version AND задание сессии принадлежит owner → env с version+1; иначе ErrStale.
	Save(ctx context.Context, env domain.Environment, owner string) (domain.Environment, error)
	SaveSecrets(ctx context.Context, id uuid.UUID, s SealedSecrets, owner string) error
	LoadSecrets(ctx context.Context, id uuid.UUID) (SealedSecrets, error)
	KnownSessions(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]domain.State, error)
	// ListByState возвращает окружения в состоянии state (для повтора очистки CLEANUP_PENDING).
	ListByState(ctx context.Context, state domain.State) ([]domain.Environment, error)
}

// PoolRepository хранит VPS пула (runtime_pool).
type PoolRepository interface {
	// ReservePool под advisory-lock добавляет VPS в PREPARING, пока PREPARING+READY с этими образом
	// и версией агента меньше size; возвращает добавленные.
	ReservePool(ctx context.Context, size int, imageID, agentVersion string) ([]domain.PoolVPS, error)
	// SavePreparing сохраняет ресурсы, host key и состояние VPS, пока она в PREPARING; иначе ErrStale.
	SavePreparing(ctx context.Context, p domain.PoolVPS) error
	// ClaimPool в одной транзакции берёт самую старую READY-VPS с этими образом и версией
	// (FOR UPDATE SKIP LOCKED), переводит её в CLAIMED и переносит ресурсы и host key в окружение
	// QUEUED → VERIFYING (те же условия, что у Save). false — READY-VPS нет.
	ClaimPool(ctx context.Context, env domain.Environment, owner, imageID, agentVersion string) (domain.Environment, bool, error)
	// MarkPoolDeleting переводит VPS в DELETING с причиной; уже DELETING или отсутствующая — no-op.
	MarkPoolDeleting(ctx context.Context, id uuid.UUID, reason string) error
	ListPool(ctx context.Context) ([]domain.PoolVPS, error)
	DeletePool(ctx context.Context, id uuid.UUID) error
}

// JobQueue — очередь заданий с lease.
type JobQueue interface {
	Claim(ctx context.Context, owner string, lease time.Duration) (Job, bool, error)
	Renew(ctx context.Context, job Job, owner string, lease time.Duration) (JobKind, error) // ErrLeaseLost
	Complete(ctx context.Context, job Job, owner string) error
	Retry(ctx context.Context, job Job, owner string, at time.Time) error
}

// CloudResource — облачный ресурс, помеченный metadata рантайма: session_id или pool_id.
type CloudResource struct {
	Kind, ID          string
	SessionID, PoolID uuid.UUID
}

// CloudProvider управляет облачными ресурсами окружения. Адаптеры оборачивают ErrCloudTransient/ErrCloudPermanent.
type CloudProvider interface {
	EnsurePort(ctx context.Context, env domain.Environment) (portID, ip string, err error)
	EnsureVolume(ctx context.Context, env domain.Environment) (string, error)                  // ждёт available
	EnsureServer(ctx context.Context, env domain.Environment, userData string) (string, error) // ждёт ACTIVE
	DeleteServer(ctx context.Context, id string) error                                         // ждёт исчезновения; 404 → nil
	DeleteVolume(ctx context.Context, id string) error
	DeletePort(ctx context.Context, id string) error
	ListOwned(ctx context.Context) ([]CloudResource, error)
}

// HostConfig — параметры подготовки VPS без сессии (prepare.yml).
type HostConfig struct {
	// Name — ID сессии или VPS пула: каталог операции и поле логов.
	Name                        uuid.UUID
	PrivateIP, HostPublicKey    string
	ServerCertPEM, ServerKeyPEM []byte
	PythonImage, KernelDNS      string
	BlockedPublicIPs            []string
}

// HostConfigurator готовит VPS и запускает агента в режиме ожидания (Ansible).
type HostConfigurator interface {
	Configure(ctx context.Context, c HostConfig) error
}

// AgentEndpoint — адрес и токен агента; пустой Token — запрос без авторизации (агент в режиме ожидания).
type AgentEndpoint struct{ BaseURL, Token string }

// AgentHealth — ответ /health агента.
type AgentHealth struct {
	// SessionID UUID runtime-сессии.
	SessionID, Language, State string
	// Ready Готовность агента принимать команды.
	Ready bool
	// Reason Безопасная причина состояния без внутренних адресов и секретов.
	Reason, Generation string
	// ActiveExecutionID ID выполняющейся команды или nil при свободном kernel.
	ActiveExecutionID *string
	// Queued Число команд, ожидающих у агента.
	Queued int
	// Draining Запрет новых команд при завершении runtime.
	Draining bool
}

// AgentClient — клиент управления агентом.
type AgentClient interface {
	Health(ctx context.Context, ep AgentEndpoint) (AgentHealth, int, error) // health, HTTP status
	// Assign назначает агента сессии (POST /assign, токен сессии — ep.Token); ответ — снимок сессии
	// после готовности kernel и HTTP status.
	Assign(ctx context.Context, ep AgentEndpoint, sessionID uuid.UUID, language string) (AgentHealth, int, error)
	Drain(ctx context.Context, ep AgentEndpoint) error
	Stop(ctx context.Context, ep AgentEndpoint) error
}

// Secrets генерирует и шифрует секреты.
type Secrets interface {
	NewToken() (string, error) // 32 random bytes → hex
	Seal(p []byte) ([]byte, error)
	Open(c []byte) ([]byte, error)
	NewHostKey() (privatePEM []byte, authorizedKey string, err error)
	IssueServerCert(ip string) (certPEM, keyPEM []byte, err error)
}

// Clock возвращает текущее время.
type Clock interface{ Now() time.Time }
