package domain

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/google/uuid"
)

// InspectRequest передаёт ID идемпотентной инспекции; код и cell_id для неё не нужны.
type InspectRequest struct {
	// ExecutionID Неизменный ID команды исполнения.
	ExecutionID string `json:"execution_id"`
}

// Validate проверяет обязательные идентификаторы и допустимые параметры запроса до записи или внешнего
// вызова.
func (r InspectRequest) Validate() error {
	if !validID(r.ExecutionID) {
		return ErrInvalid
	}
	return nil
}

// Variable содержит имя, тип и ограниченный текстовый просмотр значения переменной.
type Variable struct {
	// Name Имя переменной.
	Name string `json:"name"`
	// Type Тип переменной в интерпретаторе.
	Type string `json:"type"`
	// Preview Ограниченное текстовое представление значения переменной.
	Preview *string `json:"preview"`
}

// Inspection представляет сохранённое состояние инспекции и её переменные без привязки к ячейке.
type Inspection struct {
	// ExecutionID Неизменный ID команды исполнения.
	ExecutionID string `json:"execution_id"`
	// SessionID UUID runtime-сессии.
	SessionID uuid.UUID `json:"session_id"`
	// State Текущее состояние объекта.
	State string `json:"state"`
	// Variables Переменные, полученные завершённой инспекцией.
	Variables []Variable `json:"variables"`
	// OutputTruncated Признак сокращённого вывода, сохраняемый вместе с результатом ячейки.
	OutputTruncated bool `json:"output_truncated"`
	// Reason Безопасная причина состояния без внутренних адресов и секретов.
	Reason *string `json:"reason"`
}

// applyInspection обновляет состояние инспекции по событию, не превращая служебный вывод в результат ячейки.
func (e *Execution) applyInspection(v AgentEvent) error {
	e.Version++
	if v.Generation != "" {
		e.Generation = v.Generation
	}
	if v.Kind == "output_truncated" {
		e.Truncated = true
	}
	if v.Kind == "execution_state" {
		e.State = v.State
		e.Result = v.Result
	}
	e.Projection = "none"
	_, err := e.Inspection()
	return err
}

// Inspection собирает безопасный снимок инспекции, проверяя размер списка и длину preview переменных.
func (e Execution) Inspection() (Inspection, error) {
	out := Inspection{ExecutionID: e.ID, SessionID: e.SessionID, State: e.State, Variables: []Variable{}, OutputTruncated: e.Truncated}
	if e.Kind != "inspect" {
		return out, ErrNotFound
	}
	if out.State == "PENDING" {
		out.State = "QUEUED"
	}
	if e.State == "SUCCEEDED" {
		var r struct {
			Variables []Variable `json:"variables"`
			Truncated bool       `json:"output_truncated"`
		}
		if json.Unmarshal(e.Result, &r) != nil {
			return out, ErrProtocol
		}
		out.OutputTruncated = out.OutputTruncated || r.Truncated
		if len(r.Variables) > 200 || r.Variables == nil && !out.OutputTruncated {
			return out, ErrProtocol
		}
		for _, v := range r.Variables {
			if v.Name == "" || v.Type == "" || v.Preview != nil && utf8.RuneCountInString(*v.Preview) > 200 {
				return out, ErrProtocol
			}
		}
		if r.Variables != nil {
			out.Variables = r.Variables
		}
	} else if e.Terminal() {
		reason := "inspection_failed"
		if e.State == "UNKNOWN" {
			reason = "result_unavailable"
		}
		out.Reason = &reason
	}
	return out, nil
}
