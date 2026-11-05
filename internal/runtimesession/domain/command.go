package domain

import (
	"bytes"
	"encoding/json"
	"math"
	"unicode/utf8"
)

// CommandPayload проверяет ограничения команды и кодирует UTF-8 JSON без лишнего HTML-экранирования,
// сохраняя лимит тела агента.
func CommandPayload(c Command) ([]byte, error) {
	if utf8.RuneCountInString(c.Code) > 1048576 || !utf8.ValidString(c.Code) || math.IsNaN(c.TimeoutSeconds) || math.IsInf(c.TimeoutSeconds, 0) || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 86400 {
		return nil, ErrInvalid
	}
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if e := encoder.Encode(c); e != nil {
		return nil, ErrInvalid
	}
	if b.Len() > 2<<20 {
		return nil, ErrInvalid
	}
	return b.Bytes(), nil
}
