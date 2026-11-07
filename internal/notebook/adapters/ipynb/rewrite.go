package ipynb

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

// Rewrite меняет блоки, сохраняя остальные поля исходного ipynb и полей блоков.
// Только изменение source блока code сбрасывает его результат исполнения.
func (Codec) Rewrite(data []byte, cells []domain.Cell) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode ipynb: %w", err)
	}
	if doc == nil {
		return nil, errors.New("decode ipynb: expected object")
	}
	var originals []map[string]json.RawMessage
	if err := json.Unmarshal(doc["cells"], &originals); err != nil {
		return nil, fmt.Errorf("decode cells: %w", err)
	}
	byID := make(map[string]map[string]json.RawMessage, len(originals))
	for _, original := range originals {
		var id string
		if err := json.Unmarshal(original["id"], &id); err != nil || id == "" {
			return nil, errors.New("invalid cell id")
		}
		if _, exists := byID[id]; exists {
			return nil, fmt.Errorf("duplicate cell id %q", id)
		}
		byID[id] = original
	}
	out := make([]map[string]json.RawMessage, 0, len(cells))
	seen := make(map[string]bool, len(cells))
	for _, c := range cells {
		if c.Kind != domain.CellKindCode && c.Kind != domain.CellKindMarkdown {
			return nil, fmt.Errorf("%w: %q", ErrUnknownCellType, c.Kind)
		}
		if c.ID == "" || seen[c.ID] {
			return nil, fmt.Errorf("invalid or duplicate cell id %q", c.ID)
		}
		seen[c.ID] = true
		original, exists := byID[c.ID]
		changed := true
		if exists {
			var oldSource source
			if err := json.Unmarshal(original["source"], &oldSource); err != nil {
				return nil, fmt.Errorf("decode source: %w", err)
			}
			changed = string(oldSource) != c.Source
		} else {
			original = map[string]json.RawMessage{"metadata": json.RawMessage(`{}`)}
		}
		original["id"], _ = json.Marshal(c.ID)
		original["cell_type"], _ = json.Marshal(c.Kind)
		if changed {
			original["source"], _ = json.Marshal(c.Source)
		}
		if c.Kind == domain.CellKindCode && changed {
			original["outputs"] = json.RawMessage(`[]`)
			original["execution_count"] = json.RawMessage(`null`)
		}
		out = append(out, original)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode cells: %w", err)
	}
	doc["cells"] = encoded
	return json.Marshal(doc)
}
