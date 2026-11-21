package ipynb

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/google/uuid"
)

var importCellID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// DecodeImport проверяет байты IPYNB, возвращает документ, сохраняемые ячейки
// и число пропущенных raw-ячеек. Превышение лимита даёт domain.ErrTooLarge.
func DecodeImport(raw []byte) (domain.Document, []domain.Cell, int, error) {
	return decodeImport(raw, 128*1024*1024)
}

// decodeImport применяет общий бюджет maxBytes к входу и результату.
func decodeImport(raw []byte, maxBytes int) (domain.Document, []domain.Cell, int, error) {
	var empty domain.Document
	if len(raw) > maxBytes {
		return empty, nil, 0, domain.ErrTooLarge
	}
	value, err := decodeJSONValue(raw)
	if err != nil {
		return empty, nil, 0, errors.New("invalid IPYNB JSON or Unicode")
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return empty, nil, 0, errors.New("expected IPYNB object")
	}
	major, ok := fields["nbformat"].(json.Number)
	if !ok || major != "4" {
		return empty, nil, 0, errors.New("expected nbformat 4")
	}
	minor, ok := fields["nbformat_minor"].(json.Number)
	version, err := strconv.Atoi(string(minor))
	if !ok || err != nil || version < 0 {
		return empty, nil, 0, errors.New("nbformat_minor must be a non-negative integer")
	}
	if _, isObject := fields["metadata"].(map[string]any); !isObject {
		return empty, nil, 0, errors.New("metadata must be an object")
	}
	originals, ok := fields["cells"].([]any)
	if !ok {
		return empty, nil, 0, errors.New("cells must be an array")
	}
	if len(originals) > 1000 {
		return empty, nil, 0, domain.ErrTooLarge
	}

	kept := make([]any, 0, len(originals))
	indices := make([]int, 0, len(originals))
	for i, item := range originals {
		cell, ok := item.(map[string]any)
		if !ok {
			return empty, nil, 0, fmt.Errorf("cells[%d]: expected cell object", i)
		}
		switch cell["cell_type"] {
		case "code", "markdown":
			var src source
			if sourceErr := src.decode(cell["source"]); sourceErr != nil {
				return empty, nil, 0, fmt.Errorf("cells[%d]: %w", i, sourceErr)
			}
			if len(src) > 64*1024 {
				return empty, nil, 0, fmt.Errorf("cells[%d].source: %w", i, domain.ErrTooLarge)
			}
			cell["source"] = string(src)
			if count, ok := cell["execution_count"].(json.Number); ok {
				if _, countErr := count.Int64(); countErr != nil {
					return empty, nil, 0, fmt.Errorf("cells[%d]: execution_count must be an int64 or null", i)
				}
			}
			kept = append(kept, cell)
			indices = append(indices, i)
		case "raw":
			// Проверяем ограничения JSON без раскрытия чисел отброшенной ячейки.
			remaining := math.MaxInt
			if budgetErr := canonicalBudget(cell, &remaining); budgetErr != nil {
				return empty, nil, 0, fmt.Errorf("cells[%d]: %w", i, budgetErr)
			}
		default:
			return empty, nil, 0, fmt.Errorf("cells[%d]: %w", i, ErrUnknownCellType)
		}
	}
	fields["cells"] = kept
	normalized, err := canonicalValue(fields, maxBytes)
	if err != nil {
		return empty, nil, 0, err
	}
	doc, cells, err := decodeDocument(normalized, indices, maxBytes)
	if err != nil {
		return empty, nil, 0, err
	}
	return doc, cells, len(originals) - len(kept), nil
}

// assignImportIDs заполняет отсутствующие ID после проверки всех переданных ID.
func assignImportIDs(cells []domain.Cell, seen map[string]bool) error {
	for i := range cells {
		for cells[i].ID == "" {
			id, err := uuid.NewRandom()
			if err != nil {
				return errors.New("generate cell id")
			}
			value := id.String()
			if !seen[value] {
				cells[i].ID = value
				seen[value] = true
			}
		}
	}
	return nil
}
