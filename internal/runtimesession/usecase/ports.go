// Package usecase управляет сохраняемыми runtime-сессиями через порты без зависимостей от транспорта.
package usecase

import (
	"context"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
)

// Store задаёт операции хранения сессий, команд, журнала и владения фоновыми задачами.
type Store interface {
	// Queue читает только незавершённые исполнения ячеек: выполняемое первым, затем ожидающие; инспекции не
	// включаются.
	Queue(context.Context, int64, uuid.UUID) (domain.Queue, error)
	// AdmitBatch атомарно проверяет и сохраняет весь фиксированный пакет ячеек; повтор request_id возвращает
	// прежние IDs.
	AdmitBatch(context.Context, int64, uuid.UUID, domain.BatchRequest) (domain.Batch, error)
	// AdmitInspection сохраняет идемпотентную инспекцию в общей очереди команд, не создавая для неё фиктивную
	// ячейку.
	AdmitInspection(context.Context, int64, uuid.UUID, string) (domain.Execution, error)
	// Control сериализует управление kernel с приёмом команд; restart разрешается только при полностью
	// свободной очереди.
	Control(context.Context, int64, uuid.UUID, bool, func(context.Context) (string, error)) (string, error)
	// Active возвращает единственную незавершённую сессию пользователя либо явное отсутствие сессии.
	Active(context.Context, int64) (*domain.Session, error)
	// Start проверяет параметры и сохраняет намерение запуска пользовательской сессии до подготовки VPS.
	Start(context.Context, domain.Session) (domain.Session, error)
	// Owned возвращает сессию только указанного владельца; чужой ID не раскрывается.
	Owned(context.Context, int64, uuid.UUID) (domain.Session, error)
	// Mutate атомарно меняет принадлежащую пользователю сессию и отменяет устаревшее владение фоновой задачей.
	Mutate(context.Context, int64, uuid.UUID, func(*domain.Session) error) error
	// Admit под блокировкой проверяет владельца, состояние сессии и source_hash, затем сохраняет команду до
	// отправки агенту.
	Admit(context.Context, int64, uuid.UUID, domain.ExecuteRequest) (domain.Execution, error)
	// Execution читает сохранённое исполнение, проверяя принадлежность сессии владельцу.
	Execution(context.Context, int64, uuid.UUID, string) (domain.Execution, error)
	// Executions читает команды сессии в устойчивом порядке принятия из базы, а не по ID или номеру ячейки.
	Executions(context.Context, uuid.UUID) ([]domain.Execution, error)
	// Claim выбирает готовую к обработке сессию и временно закрепляет её за одним фоновым обработчиком.
	Claim(context.Context, string) (domain.Lease, error)
	// CheckLease проверяет, что фоновый обработчик ещё владеет задачей и может выполнять внешние действия.
	CheckLease(context.Context, domain.Lease) error
	// Save сохраняет шаг обработки сессии и освобождает её для следующего прохода.
	Save(context.Context, domain.Lease, domain.Session) error
	// Checkpoint сохраняет промежуточное состояние сессии, не освобождая текущее владение задачей.
	Checkpoint(context.Context, domain.Lease, domain.Session) error
	// SaveExecution сохраняет состояние команды только при действующем владении фоновой задачей и ставит
	// завершённый результат на запись в ячейку.
	SaveExecution(context.Context, domain.Lease, domain.Execution) error
	// CommitEvents одной транзакцией сохраняет события, обновляет результаты и курсор; уже записанные seq
	// повторно не применяются.
	CommitEvents(context.Context, domain.Lease, []domain.AgentEvent) error
	// Events возвращает события после указанного курсора; чтение не продлевает активность пользователя.
	Events(context.Context, int64, uuid.UUID, int64, domain.EventLimits) (domain.EventPage, error)
	// Project применяет отложенный результат к актуальной ячейке под блокировкой блокнота, защищая более новую
	// попытку и изменённый код.
	Project(context.Context, OutputWriter) error
}

// Environments отделяет управление пользовательской сессией от создания и удаления VPS в модуле #57.
type Environments interface {
	// RequestCreate сохраняет запрос подготовки окружения через готовый сервис #57, не ожидая создания VPS.
	RequestCreate(context.Context, domain.EnvironmentRequest) (domain.Environment, error)
	// Get читает состояние окружения по UUID сессии.
	Get(context.Context, uuid.UUID) (domain.Environment, error)
	// RequestDelete запрашивает удаление окружения; подтверждение запроса ещё не означает завершение очистки.
	RequestDelete(context.Context, uuid.UUID, string) error
}

// SessionAgent определяет внутреннее общение с агентом; его токены и адреса остаются внутри адаптера.
type SessionAgent interface {
	// Health читает готовность агента, поколение kernel и состояние его очереди.
	Health(context.Context, uuid.UUID) (domain.Health, error)
	// Execute передаёт сохранённую команду агенту; неизменный ID позволяет безопасно повторить сетевой запрос.
	Execute(context.Context, uuid.UUID, domain.Command) (domain.AgentExecution, error)
	// Execution читает состояние и результат команды у агента.
	Execution(context.Context, uuid.UUID, string) (domain.AgentExecution, error)
	// Events читает журнал агента после его внутреннего seq.
	Events(context.Context, uuid.UUID, int64, domain.EventLimits) ([]domain.AgentEvent, error)
	// Activity передаёт агенту реальное действие пользователя, продлевающее время активности.
	Activity(context.Context, uuid.UUID) error
	// Drain запрещает новые команды у агента и запрашивает остановку выполняющейся команды перед завершением.
	Drain(context.Context, uuid.UUID) error
	// Stop просит агента остановить kernel, сохранив доступ к журналу.
	Stop(context.Context, uuid.UUID) error
}

// Clock позволяет подменять текущее время в тестах восстановления и жизненного цикла.
type Clock func() time.Time

// OutputWriter сохраняет результат в существующем ipynb и возвращает новый файловый ключ и признак
// применения.
type OutputWriter interface {
	// ApplyOutput проверяет ID и хэш исходника, записывает результат в новый S3-объект и возвращает его ключ;
	// публикацию ссылки выполняет вызывающая транзакция.
	ApplyOutput(context.Context, string, domain.Execution) (string, bool, error)
}

// KernelAgent отделяет команды управления памятью kernel от исполнения кода и чтения событий.
type KernelAgent interface {
	// Interrupt запрашивает прерывание текущей команды без автоматического повтора при потере ответа.
	Interrupt(context.Context, uuid.UUID) error
	// Restart перезапускает свободный kernel и возвращает новое поколение; потерянный ответ автоматически не
	// повторяется.
	Restart(context.Context, uuid.UUID) (string, error)
}
