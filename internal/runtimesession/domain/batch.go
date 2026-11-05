package domain

import (
	"github.com/google/uuid"
)

// MaxQueuedCommands ограничивает общую очередь backend тысячей незавершённых команд, включая инспекции.
const MaxQueuedCommands = 1000

// validCellID проверяет устойчивый ID ячейки: от 1 до 64 латинских букв, цифр, дефисов или подчёркиваний.
func validCellID(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
		if !valid {
			return false
		}
	}
	return true
}

// BatchCell связывает ID ячейки с хэшем сохранённого исходника для фиксированного массового запуска.
type BatchCell struct {
	// CellID Устойчивый ID ячейки внутри блокнота.
	CellID string `json:"cell_id"`
	// SourceHash Хэш сохранённого исходника для защиты от исполнения устаревшего кода.
	SourceHash string `json:"source_hash"`
}

// BatchRequest описывает один идемпотентный массовый запуск с зафиксированными ячейками и таймаутом каждой
// команды.
type BatchRequest struct {
	// RequestID Ключ идемпотентности пользовательского запроса.
	RequestID string `json:"request_id"`
	// Cells Ячейки фиксированного снимка в выбранном порядке.
	Cells []BatchCell `json:"cells"`
	// TimeoutSeconds Допустимое время выполнения команды в секундах.
	TimeoutSeconds float64 `json:"timeout_seconds"`
}

// Batch содержит полный неизменный список принятых execution_ids, включая уже завершённые попытки.
type Batch struct {
	// RequestID Ключ идемпотентности пользовательского запроса.
	RequestID string `json:"request_id"`
	// SessionID UUID runtime-сессии.
	SessionID uuid.UUID `json:"session_id"`
	// ExecutionIDs ID всех исполнений принятого пакета в исходном порядке.
	ExecutionIDs []string `json:"execution_ids"`
}

// Validate проверяет обязательные идентификаторы и допустимые параметры запроса до записи или внешнего
// вызова.
func (r BatchRequest) Validate() error {
	if !validID(r.RequestID) || len(r.Cells) < 1 || len(r.Cells) > MaxQueuedCommands {
		return ErrInvalid
	}
	seen := make(map[string]bool, len(r.Cells))
	for _, c := range r.Cells {
		if !validCellID(c.CellID) || seen[c.CellID] {
			return ErrInvalid
		}
		seen[c.CellID] = true
		if err := (ExecuteRequest{ExecutionID: r.RequestID, CellID: c.CellID, SourceHash: c.SourceHash, TimeoutSeconds: r.TimeoutSeconds}).Validate(); err != nil {
			return err
		}
	}
	return nil
}

// QueueItem представляет незавершённое исполнение конкретной ячейки, а не инспекцию переменных.
type QueueItem struct {
	// ExecutionID Неизменный ID команды исполнения.
	ExecutionID string `json:"execution_id"`
	// CellID Устойчивый ID ячейки внутри блокнота.
	CellID string `json:"cell_id"`
	// State Текущее состояние объекта.
	State string `json:"state"`
}

// Queue возвращает текущие исполнения ячеек в порядке работы; пустой список не означает завершение сессии.
type Queue struct {
	// SessionID UUID runtime-сессии.
	SessionID uuid.UUID `json:"session_id"`
	// Items Незавершённые исполнения ячеек без инспекций переменных.
	Items []QueueItem `json:"items"`
}
