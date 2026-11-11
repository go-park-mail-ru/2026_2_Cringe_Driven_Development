// Package domain описывает сохраняемые runtime-сессии и неизменные параметры исполнений.
package domain

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrNotFound обозначает отсутствие ресурса или отсутствие прав владельца на него.
	ErrNotFound = errors.New("runtime not found")
	// ErrConflict обозначает несовместимый повтор команды, изменение исходника или недопустимое состояние
	// сессии.
	ErrConflict = errors.New("runtime conflict")
	// ErrInvalid обозначает некорректные параметры пользовательской или внутренней команды.
	ErrInvalid = errors.New("invalid runtime request")
	// ErrUnavailable обозначает временно недоступный runtime или отключённое создание новых сессий.
	ErrUnavailable = errors.New("runtime unavailable")
	// ErrLeaseLost запрещает устаревшему обработчику продолжать внешние действия или сохранять результат.
	ErrLeaseLost = errors.New("runtime lease lost")
	// ErrProtocol обозначает несогласованные или повреждённые данные внутреннего агента.
	ErrProtocol = errors.New("runtime protocol error")
	// ErrQueueFull обозначает исчерпание допустимой очереди команд.
	ErrQueueFull = errors.New("runtime queue full")
)

// State обозначает жизненный цикл пользовательской runtime-сессии, а не результат отдельной команды.
type State string

const (
	// Provisioning означает, что окружение принято в подготовку и исполнение ещё недоступно.
	Provisioning State = "PROVISIONING"
	// Starting означает настройку или проверку запуска агента перед допуском команд.
	Starting State = "STARTING"
	// Ready означает готовый kernel и возможность принимать пользовательские команды.
	Ready State = "READY"
	// Reconnecting означает восстановление связи без автоматического признания исполнения неуспешным.
	Reconnecting State = "RECONNECTING"
	// Stopping означает незавершённую остановку и очистку; пользовательский слот ещё занят.
	Stopping State = "STOPPING"
	// Stopped означает подтверждённое штатное завершение пользовательской сессии.
	Stopped State = "STOPPED"
	// Failed означает конечное завершение сессии с ошибкой.
	Failed State = "FAILED"
)

// Active показывает, занимает ли состояние пользовательский слот; STOPPING остаётся активным до очистки.
func (s State) Active() bool { return s != Stopped && s != Failed }

// StartRequest содержит ключ повторного запроса запуска и язык единственного kernel сессии.
type StartRequest struct {
	// RequestID Ключ идемпотентности пользовательского запроса.
	RequestID string `json:"request_id"`
	// Language Язык kernel: только python.
	Language string `json:"language"`
}

// Validate проверяет обязательные идентификаторы и допустимые параметры запроса до записи или внешнего
// вызова.
func (r StartRequest) Validate() error {
	if !validID(r.RequestID) || r.Language != "python" {
		return ErrInvalid
	}
	return nil
}

// ExecuteRequest задаёт исполнение сохранённой ячейки с проверкой source_hash и таймаутом.
type ExecuteRequest struct {
	// ExecutionID Неизменный ID команды исполнения.
	ExecutionID string `json:"execution_id"`
	// CellID Устойчивый ID ячейки внутри блокнота.
	CellID string `json:"cell_id"`
	// SourceHash Хэш сохранённого исходника для защиты от исполнения устаревшего кода.
	SourceHash string `json:"source_hash"`
	// TimeoutSeconds Допустимое время выполнения команды в секундах.
	TimeoutSeconds float64 `json:"timeout_seconds"`
}

// Validate проверяет обязательные идентификаторы и допустимые параметры запроса до записи или внешнего
// вызова.
func (r ExecuteRequest) Validate() error {
	h, err := hex.DecodeString(r.SourceHash)
	if !validID(r.ExecutionID) || !validID(r.CellID) || err != nil || len(h) != 32 || r.SourceHash != strings.ToLower(r.SourceHash) || math.IsNaN(r.TimeoutSeconds) || math.IsInf(r.TimeoutSeconds, 0) || r.TimeoutSeconds < 1 || r.TimeoutSeconds > 86400 {
		return ErrInvalid
	}
	return nil
}

// validID отклоняет пустые и слишком длинные ID, пробелы по краям и управляющие разделители.
func validID(s string) bool {
	return len(s) > 0 && len(s) <= 128 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}

// Session связывает пользователя, блокнот и окружение, сохраняя также состояние фоновой обработки.
type Session struct {
	// DeleteSent Признак отправленного запроса удаления окружения.
	DeleteSent bool `json:"-"`
	// AgentStarted Признак ранее подтверждённого запуска агента.
	AgentStarted bool `json:"-"`
	// StopRetry Число попыток остановки агента.
	StopRetry int `json:"-"`
	// JournalRetry Число неудачных попыток чтения журнала.
	JournalRetry int `json:"-"`
	// JournalIncomplete Признак завершения с неполным журналом после недоступности агента.
	JournalIncomplete bool `json:"journal_incomplete"`
	// ID Идентификатор записи.
	ID uuid.UUID `json:"id"`
	// OwnerID ID владельца для проверки доступа.
	OwnerID int64 `json:"-"`
	// NotebookID ID связанного блокнота.
	NotebookID int64 `json:"notebook_id"`
	// RequestID Ключ идемпотентности пользовательского запроса.
	RequestID string `json:"-"`
	// Language Язык kernel: только python.
	Language string `json:"language"`
	// State Текущее состояние объекта.
	State State `json:"state"`
	// Stage Этап подготовки или завершения сессии для интерфейса.
	Stage string `json:"stage"`
	// Reason Безопасная причина состояния без внутренних адресов и секретов.
	Reason string `json:"reason,omitempty"`
	// CreatedAt Время создания записи.
	CreatedAt time.Time `json:"created_at"`
	// LastActivityAt Время последнего действия пользователя, без учёта фонового опроса.
	LastActivityAt time.Time `json:"last_activity_at"`
	// ActivityPending Необходимость передать агенту ещё не доставленное действие пользователя.
	ActivityPending bool `json:"-"`
	// AgentSeq Последний номер события агента, подтверждённый транзакцией архива.
	AgentSeq int64 `json:"-"`
	// PublicSeq Последний курсор публичного журнала backend.
	PublicSeq int64 `json:"cursor"`
	// Generation UUID поколения kernel, меняющийся при перезапуске.
	Generation string `json:"generation,omitempty"`
	// CreateSent Признак отправленного запроса подготовки окружения.
	CreateSent bool `json:"-"`
	// CreateUnknown Признак потери ответа создания, при котором повторяется прежний UUID.
	CreateUnknown bool `json:"-"`
	// Retry Счётчик повторных попыток текущего этапа.
	Retry int `json:"-"`
	// NextCheck Время следующего шага фоновой обработки.
	NextCheck time.Time `json:"-"`
	// CleanupError Признак ещё не подтверждённой очистки ресурсов.
	CleanupError bool `json:"cleanup_pending"`
	// JournalArchived Признак сохранения доступного журнала перед удалением окружения.
	JournalArchived bool `json:"-"`
	// LeaseVersion Версия владения задачей, защищающая от устаревшего обработчика.
	LeaseVersion int64 `json:"-"`
}

// Execution хранит неизменные параметры попытки и накопленный результат; служебные поля не выдаются через
// API.
type Execution struct {
	// SourceVersion идентифицирует точный сохранённый исходник, допущенный к выполнению.
	SourceVersion string `json:"-"`
	// Kind Вид команды, события или ячейки, определяющий обработку.
	Kind string `json:"-"`
	// SessionID UUID runtime-сессии.
	SessionID uuid.UUID `json:"session_id"`
	// ID Идентификатор записи.
	ID string `json:"execution_id"`
	// OwnerID ID владельца для проверки доступа.
	OwnerID int64 `json:"-"`
	// NotebookID ID связанного блокнота.
	NotebookID int64 `json:"-"`
	// CellID Устойчивый ID ячейки внутри блокнота.
	CellID string `json:"cell_id"`
	// SourceHash Хэш сохранённого исходника для защиты от исполнения устаревшего кода.
	SourceHash string `json:"source_hash"`
	// Code Снимок исходника, который должен выполнить агент.
	Code string `json:"-"`
	// TimeoutSeconds Допустимое время выполнения команды в секундах.
	TimeoutSeconds float64 `json:"-"`
	// Attempt Порядковый номер попытки для данной ячейки.
	Attempt int64 `json:"attempt"`
	// Dispatch Служебное состояние доставки команды агенту.
	Dispatch string `json:"-"`
	// State Текущее состояние объекта.
	State string `json:"state"`
	// Result Результат команды в исходном JSON-представлении.
	Result json.RawMessage `json:"result,omitempty"`
	// Outputs Сохранённые сообщения вывода в формате ipynb.
	Outputs []json.RawMessage `json:"outputs"`
	// ExecutionCount Счётчик исполнения kernel; nil означает отсутствие известного номера.
	ExecutionCount *int64 `json:"execution_count"`
	// Truncated Признак усечения данных из-за ограничения размера.
	Truncated bool `json:"output_truncated"`
	// ClearPending Отложенная очистка вывода до следующего сообщения при clear_output с wait=true.
	ClearPending bool `json:"-"`
	// Version Версия состояния для защиты согласованного обновления.
	Version int64 `json:"-"`
	// Projection Состояние записи накопленного результата в ячейку блокнота.
	Projection string `json:"projection"`
	// Generation UUID поколения kernel, меняющийся при перезапуске.
	Generation string `json:"-"`
}

// Same проверяет, совпадает ли повтор запроса с ранее сохранёнными параметрами исполнения.
func (e Execution) Same(r ExecuteRequest) bool {
	return e.ID == r.ExecutionID && e.CellID == r.CellID && e.SourceHash == r.SourceHash && e.TimeoutSeconds == r.TimeoutSeconds
}

// Terminal проверяет, завершилась ли попытка; UNKNOWN также завершён, но не означает успешное выполнение.
func (e Execution) Terminal() bool {
	switch e.State {
	case "SUCCEEDED", "FAILED", "INTERRUPTED", "TIMED_OUT", "CANCELLED", "UNKNOWN", "REJECTED":
		return true
	}
	return false
}

// AgentEvent описывает событие внутреннего журнала агента с исходными полями транспорта Jupyter.
type AgentEvent struct {
	// Channel Канал сообщения Jupyter, например iopub.
	Channel string `json:"channel,omitempty"`
	// ParentMsgID ID родительского сообщения Jupyter для связи с запросом.
	ParentMsgID string `json:"parent_msg_id,omitempty"`
	// Buffers Бинарные части сообщения, закодированные в base64.
	Buffers []string `json:"buffers,omitempty"`
	// Timestamp Время события агента в секундах Unix.
	Timestamp float64 `json:"timestamp,omitempty"`
	// Seq Номер события в соответствующем журнале.
	Seq int64 `json:"seq"`
	// SessionID UUID runtime-сессии.
	SessionID uuid.UUID `json:"session_id"`
	// ExecutionID Неизменный ID команды исполнения.
	ExecutionID string `json:"execution_id"`
	// Kind Вид команды, события или ячейки, определяющий обработку.
	Kind string `json:"kind"`
	// State Текущее состояние объекта.
	State string `json:"state,omitempty"`
	// Reason Безопасная причина состояния без внутренних адресов и секретов.
	Reason string `json:"reason,omitempty"`
	// Generation UUID поколения kernel, меняющийся при перезапуске.
	Generation string `json:"generation,omitempty"`
	// MsgType Тип сообщения Jupyter, определяющий формат content.
	MsgType string `json:"msg_type,omitempty"`
	// Content Содержимое сообщения Jupyter в JSON.
	Content json.RawMessage `json:"content,omitempty"`
	// Metadata Дополнительные метаданные сообщения или ячейки.
	Metadata json.RawMessage `json:"metadata,omitempty"`
	// Result Результат команды в исходном JSON-представлении.
	Result json.RawMessage `json:"result,omitempty"`
}

// Event представляет публичное событие backend со своим курсором, отдельным от seq агента.
type Event struct {
	// Seq Номер события в соответствующем журнале.
	Seq int64 `json:"seq"`
	// Kind Вид команды, события или ячейки, определяющий обработку.
	Kind string `json:"kind"`
	// Body JSON-тело публичного события.
	Body json.RawMessage `json:"body"`
}

// Health содержит проверяемое назначение и готовность агента, включая текущую команду и очередь.
type Health struct {
	// SessionID UUID runtime-сессии.
	SessionID uuid.UUID `json:"session_id"`
	// Language Язык kernel: только python.
	Language string `json:"language"`
	// State Текущее состояние объекта.
	State string `json:"state"`
	// Reason Безопасная причина состояния без внутренних адресов и секретов.
	Reason string `json:"reason"`
	// Generation UUID поколения kernel, меняющийся при перезапуске.
	Generation string `json:"generation"`
	// Ready Готовность агента принимать команды.
	Ready bool `json:"ready"`
	// Draining Запрет новых команд при завершении runtime.
	Draining bool `json:"draining"`
	// ActiveExecutionID ID выполняющейся команды или nil при свободном kernel.
	ActiveExecutionID *string `json:"active_execution_id"`
	// Queued Число команд, ожидающих у агента.
	Queued int `json:"queued"`
}

// Command передаёт агенту сохранённый ID, код и таймаут; Kind выбирает обычное исполнение или инспекцию.
type Command struct {
	// Kind Вид команды, события или ячейки, определяющий обработку.
	Kind string `json:"-"`
	// ExecutionID Неизменный ID команды исполнения.
	ExecutionID string `json:"execution_id"`
	// Code Снимок исходника, который должен выполнить агент.
	Code string `json:"code"`
	// TimeoutSeconds Допустимое время выполнения команды в секундах.
	TimeoutSeconds float64 `json:"timeout_seconds"`
}

// AgentExecution содержит подтверждение агента и доступный результат команды после принятия или повторного
// чтения.
type AgentExecution struct {
	// ExecutionID Неизменный ID команды исполнения.
	ExecutionID string `json:"execution_id"`
	// State Текущее состояние объекта.
	State string `json:"state"`
	// Result Результат команды в исходном JSON-представлении.
	Result json.RawMessage `json:"result"`
}

// Lease связывает снимок сессии с версией временного владения задачей фоновым обработчиком.
type Lease struct {
	// Session Снимок runtime-сессии.
	Session Session
	// Version Версия состояния для защиты согласованного обновления.
	Version int64
}

// ErrActiveSessionExists обозначает занятый слот пользователя; подробности доступны в ActiveSessionError.
var ErrActiveSessionExists = errors.New("active runtime session exists")

// ActiveSessionError передаёт конфликт запуска вместе с уже занятой сессией для сообщения пользователю.
type ActiveSessionError struct{ Session Session }

// Error возвращает безопасное текстовое описание ошибки.
func (e *ActiveSessionError) Error() string { return ErrActiveSessionExists.Error() }

// Unwrap раскрывает исходную ошибку для errors.Is и errors.As без потери классификации.
func (e *ActiveSessionError) Unwrap() error { return ErrActiveSessionExists }

// EnvironmentRequest задаёт UUID, язык и серверный профиль при обращении к готовому сервису окружений.
type EnvironmentRequest struct {
	// SessionID UUID runtime-сессии.
	SessionID uuid.UUID
	// Language Язык kernel: только python.
	Language, Profile string
}

// Environment содержит только безопасные сведения о подготовке окружения, необходимые runtime-сессии.
type Environment struct {
	// SessionID UUID runtime-сессии.
	SessionID uuid.UUID
	// State Текущее состояние объекта.
	State, ReasonCode string
}

// EventLimits ограничивает число и общий размер событий в одной странице журнала.
type EventLimits struct{ Count, Bytes int }

// EventPage объединяет страницу публичных событий, следующий курсор и признак оставшихся данных.
type EventPage struct {
	// Events События текущей страницы в порядке курсора.
	Events []Event
	// NextCursor Курсор для следующего опроса событий.
	NextCursor int64
	// HasMore Наличие событий после текущей страницы.
	HasMore bool
}
