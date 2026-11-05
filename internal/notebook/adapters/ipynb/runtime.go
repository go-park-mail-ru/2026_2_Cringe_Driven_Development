package ipynb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
)

// RuntimeFiles вызывается под общей блокировкой строки блокнота.
// Прежний объект неизменяем: новая ссылка публикуется транзакцией projector.
type RuntimeFiles struct {
	// Files Доступ к файловому хранилищу блокнотов.
	Files interface {
		Get(context.Context, string) ([]byte, error)
		Put(context.Context, string, []byte) error
	}
}

// ReadCode читает сохранённый исходник кодовой ячейки по её устойчивому ID.
func (r RuntimeFiles) ReadCode(ctx context.Context, key, cellID string) (string, error) {
	raw, err := r.Files.Get(ctx, key)
	if err != nil {
		return "", err
	}
	cells, err := Parse(raw)
	if err != nil {
		return "", err
	}
	for _, c := range cells {
		if c.ID == cellID {
			if c.Kind != "code" {
				return "", D.ErrInvalid
			}
			return c.Source, nil
		}
	}
	return "", D.ErrNotFound
}

// ApplyOutput проверяет ID и хэш исходника, записывает результат в новый S3-объект и возвращает его ключ;
// публикацию ссылки выполняет вызывающая транзакция.
func (r RuntimeFiles) ApplyOutput(ctx context.Context, key string, x D.Execution) (string, bool, error) {
	raw, err := r.Files.Get(ctx, key)
	if err != nil {
		return "", false, err
	}
	var doc map[string]json.RawMessage
	if err = json.Unmarshal(raw, &doc); err != nil {
		return "", false, err
	}
	if doc == nil {
		return "", false, D.ErrInvalid
	}
	var cells []map[string]json.RawMessage
	if err = json.Unmarshal(doc["cells"], &cells); err != nil {
		return "", false, err
	}
	for _, c := range cells {
		var id, kind string
		if err = json.Unmarshal(c["id"], &id); err != nil {
			return "", false, err
		}
		if id != x.CellID {
			continue
		}
		if err = json.Unmarshal(c["cell_type"], &kind); err != nil {
			return "", false, err
		}
		if kind != "code" {
			return "", false, nil
		}
		var src source
		if err = json.Unmarshal(c["source"], &src); err != nil {
			return "", false, err
		}
		sum := sha256.Sum256([]byte(src))
		if hex.EncodeToString(sum[:]) != x.SourceHash {
			return "", false, nil
		}
		outs := make([]map[string]json.RawMessage, 0, len(x.Outputs))
		for _, out := range x.Outputs {
			var fields map[string]json.RawMessage
			if err = json.Unmarshal(out, &fields); err != nil {
				return "", false, err
			}
			if fields == nil {
				return "", false, D.ErrProtocol
			}
			delete(fields, "transient")
			outs = append(outs, fields)
		}
		c["outputs"], err = json.Marshal(outs)
		if err != nil {
			return "", false, err
		}
		c["execution_count"], err = json.Marshal(x.ExecutionCount)
		if err != nil {
			return "", false, err
		}
		metadata := map[string]json.RawMessage{}
		if m := c["metadata"]; len(m) > 0 {
			if err = json.Unmarshal(m, &metadata); err != nil {
				return "", false, err
			}
		}
		if metadata == nil {
			metadata = map[string]json.RawMessage{}
		}
		metadata["cellestial_runtime"], err = json.Marshal(map[string]any{"execution_id": x.ID, "session_id": x.SessionID, "output_truncated": x.Truncated})
		if err != nil {
			return "", false, err
		}
		c["metadata"], err = json.Marshal(metadata)
		if err != nil {
			return "", false, err
		}
		doc["cells"], err = json.Marshal(cells)
		if err != nil {
			return "", false, err
		}
		updated, err := json.Marshal(doc)
		if err != nil {
			return "", false, err
		}
		newKey := fmt.Sprintf("runtime/%s/%s.ipynb", x.SessionID, uuid.NewString())
		if err = r.Files.Put(ctx, newKey, updated); err != nil {
			return "", false, err
		}
		return newKey, true, nil
	}
	return "", false, nil
}

// ReadBatch читает один снимок исходников и проверяет, что выбранные ячейки сохранили свой порядок.
func (r RuntimeFiles) ReadBatch(ctx context.Context, key string, ids []string) ([]string, error) {
	raw, err := r.Files.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	cells, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	positions := map[string]int{}
	for i, c := range cells {
		positions[c.ID] = i
	}
	out := make([]string, 0, len(ids))
	previous := -1
	for _, id := range ids {
		pos, ok := positions[id]
		if !ok || pos <= previous {
			return nil, D.ErrConflict
		}
		c := cells[pos]
		if c.Kind != "code" {
			return nil, D.ErrInvalid
		}
		previous = pos
		out = append(out, c.Source)
	}
	return out, nil
}
