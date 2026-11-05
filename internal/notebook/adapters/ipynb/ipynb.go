// Package ipynb переводит файл .ipynb (nbformat 4.5) в блоки и обратно.
package ipynb

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

// ErrUnknownCellType возвращается на вид блока, который бэкенд не пишет.
var ErrUnknownCellType = errors.New("unknown cell_type")

// file описывает только те поля .ipynb, которые нужны бэкенду.
type file struct {
	Cells         []cell   `json:"cells"`
	Metadata      struct{} `json:"metadata"`
	NBFormat      int      `json:"nbformat"`
	NBFormatMinor int      `json:"nbformat_minor"`
}

// cell читает нужные поля ячейки ipynb, включая сохранённый результат и признак усечения в метаданных.
type cell struct {
	// ID Идентификатор записи.
	ID string `json:"id"`
	// CellType Тип ячейки в формате ipynb.
	CellType domain.CellKind `json:"cell_type"`
	// Metadata Дополнительные метаданные сообщения или ячейки.
	Metadata struct {
		Runtime *struct {
			Truncated bool `json:"output_truncated"`
		} `json:"cellestial_runtime,omitempty"`
	} `json:"metadata"`
	// Source Исходный текст ячейки.
	Source source `json:"source"`
	// Outputs Сохранённые сообщения вывода в формате ipynb.
	Outputs json.RawMessage `json:"outputs,omitempty"`
	// ExecutionCount Счётчик исполнения kernel; nil означает отсутствие известного номера.
	ExecutionCount json.RawMessage `json:"execution_count,omitempty"`
}

// source в файле бывает строкой или массивом строк.
type source string

// UnmarshalJSON склеивает массив строк в одну строку.
func (s *source) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		*s = source(str)
		return nil
	}
	var lines []string
	if err := json.Unmarshal(data, &lines); err != nil {
		return fmt.Errorf("source is neither a string nor an array of strings: %w", err)
	}
	*s = source(strings.Join(lines, ""))
	return nil
}

// Parse разбирает файл в блоки; без блоков возвращает пустой срез, а не nil.
func Parse(data []byte) ([]domain.Cell, error) {
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("decode ipynb: %w", err)
	}
	cells := make([]domain.Cell, 0, len(f.Cells))
	for _, c := range f.Cells {
		if c.CellType != domain.CellKindCode && c.CellType != domain.CellKindMarkdown {
			return nil, fmt.Errorf("%w: %q", ErrUnknownCellType, c.CellType)
		}
		v := domain.Cell{ID: c.ID, Kind: c.CellType, Source: string(c.Source)}
		if c.CellType == domain.CellKindCode {
			var result domain.CellResult
			if len(c.Outputs) > 0 && json.Unmarshal(c.Outputs, &result.Outputs) != nil {
				return nil, errors.New("invalid cell outputs")
			}
			if len(c.ExecutionCount) > 0 && json.Unmarshal(c.ExecutionCount, &result.ExecutionCount) != nil {
				return nil, errors.New("invalid execution_count")
			}
			if c.Metadata.Runtime != nil {
				result.Truncated = c.Metadata.Runtime.Truncated
			}
			if len(result.Outputs) > 0 || result.ExecutionCount != nil || result.Truncated {
				v.Result = &result
			}
		}
		cells = append(cells, v)
	}
	return cells, nil
}

// Build собирает файл nbformat 4.5 из блоков.
func Build(cells []domain.Cell) ([]byte, error) {
	f := file{
		Cells:         make([]cell, 0, len(cells)),
		NBFormat:      4,
		NBFormatMinor: 5,
	}
	for _, c := range cells {
		out := cell{ID: c.ID, CellType: c.Kind, Source: source(c.Source)}
		switch c.Kind {
		case domain.CellKindCode:
			out.Outputs = json.RawMessage("[]")
			out.ExecutionCount = json.RawMessage("null")
		case domain.CellKindMarkdown:
		default:
			return nil, fmt.Errorf("%w: %q", ErrUnknownCellType, c.Kind)
		}
		f.Cells = append(f.Cells, out)
	}
	return json.MarshalIndent(f, "", " ")
}

// Codec преобразует блоки в файл nbformat 4.5 и обратно.
type Codec struct{}

func (Codec) Parse(data []byte) ([]domain.Cell, error)  { return Parse(data) }
func (Codec) Build(cells []domain.Cell) ([]byte, error) { return Build(cells) }
