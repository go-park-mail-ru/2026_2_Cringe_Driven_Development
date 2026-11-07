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

type cell struct {
	ID             string          `json:"id"`
	CellType       domain.CellKind `json:"cell_type"`
	Metadata       struct{}        `json:"metadata"`
	Source         source          `json:"source"`
	Outputs        json.RawMessage `json:"outputs,omitempty"`
	ExecutionCount json.RawMessage `json:"execution_count,omitempty"`
}

// source в файле бывает строкой или массивом строк.
type source string

// UnmarshalJSON склеивает массив строк в одну строку.
func (s *source) UnmarshalJSON(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	switch v := value.(type) {
	case string:
		*s = source(v)
		return nil
	case []any:
		var text strings.Builder
		for _, part := range v {
			line, ok := part.(string)
			if !ok {
				return errors.New("source array must contain only strings")
			}
			text.WriteString(line)
		}
		*s = source(text.String())
		return nil
	default:
		return errors.New("source must be a string or an array of strings")
	}
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
		cells = append(cells, domain.Cell{ID: c.ID, Kind: c.CellType, Source: string(c.Source)})
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
