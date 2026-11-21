package ipynb

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

// CanonicalJSON использует компактное экранирование encoding/json, отсортированные ключи
// объектов, UTF-8 и десятичные числа без экспоненты и незначащих хвостовых нулей.
// Нормализация чисел делает учёт размера независимым от записи чисел в jsonb.
func CanonicalJSON(raw []byte) (json.RawMessage, error) {
	return CanonicalJSONLimit(raw, 128*1024*1024)
}

// CanonicalJSONLimit проверяет полный закодированный размер до раскрытия чисел
// и выделения итогового JSON. Лимит учитывает экранирование и разделители.
func CanonicalJSONLimit(raw []byte, limit int) (json.RawMessage, error) {
	if limit < 0 {
		return nil, domain.ErrTooLarge
	}
	value, err := decodeJSONValue(raw)
	if err != nil {
		return nil, err
	}
	return canonicalValue(value, limit)
}

// decodeJSONValue читает одно JSON-значение без потери точности чисел и Unicode.
func decodeJSONValue(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("invalid UTF-8")
	}
	// encoding/json заменяет непарные суррогаты UTF-16. Отклоняем их до декодирования.
	for i := 0; i < len(raw); i++ {
		if raw[i] != '"' {
			continue
		}
		i++
		for ; i < len(raw) && raw[i] != '"'; i++ {
			if raw[i] != '\\' {
				continue
			}
			i++
			if i >= len(raw) {
				break
			}
			if raw[i] != 'u' {
				continue
			}
			if i+4 >= len(raw) {
				return nil, errors.New("invalid unicode escape")
			}
			n, e := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
			if e != nil {
				return nil, e
			}
			i += 4
			if n >= 0xD800 && n <= 0xDFFF {
				if n > 0xDBFF || i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
					return nil, errors.New("unpaired surrogate")
				}
				low, e := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
				if e != nil || low < 0xDC00 || low > 0xDFFF {
					return nil, errors.New("unpaired surrogate")
				}
				i += 6
			}
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	return value, nil
}

// canonicalValue проверяет размер перед сериализацией разобранного JSON.
func canonicalValue(value any, limit int) (json.RawMessage, error) {
	remaining := limit
	if err := canonicalBudget(value, &remaining); err != nil {
		return nil, err
	}
	value, err := normalize(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// normalize приводит числа и рекурсивные значения к форме экспорта IPYNB.
func normalize(v any) (any, error) {
	switch x := v.(type) {
	case string:
		if strings.ContainsRune(x, 0) {
			return nil, errors.New("NUL is not supported by PostgreSQL")
		}
	case json.Number:
		digits, point, negative, err := decimalLayout(x)
		if err != nil {
			return nil, err
		}
		var s string
		switch {
		case point <= 0:
			s = "0." + strings.Repeat("0", -point) + digits
		case point >= len(digits):
			s = digits + strings.Repeat("0", point-len(digits))
		default:
			s = digits[:point] + "." + digits[point:]
		}
		if negative {
			s = "-" + s
		}
		return json.Number(s), nil
	case []any:
		for i, item := range x {
			n, e := normalize(item)
			if e != nil {
				return nil, e
			}
			x[i] = n
		}
	case map[string]any:
		for k, item := range x {
			if strings.ContainsRune(k, 0) {
				return nil, errors.New("NUL JSON key")
			}
			n, e := normalize(item)
			if e != nil {
				return nil, e
			}
			x[k] = n
		}
	}
	return v, nil
}

// decimalLayout вычисляет форму канонического десятичного числа, не раскрывая
// экспоненту. Синтаксис JSON уже проверен encoding/json.
func decimalLayout(n json.Number) (digits string, point int, negative bool, err error) {
	s := string(n)
	negative = strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp, err = strconv.Atoi(s[i+1:])
		if err != nil || exp > 131071 || exp < -16383 {
			return "", 0, false, errors.New("number outside PostgreSQL range")
		}
		s = s[:i]
	}
	point = len(s)
	scale := 0
	if i := strings.IndexByte(s, '.'); i >= 0 {
		point = i
		scale = len(s) - i - 1
	}
	if point+exp > 131072 || scale-exp > 16383 {
		return "", 0, false, errors.New("number outside PostgreSQL range")
	}
	digits = strings.ReplaceAll(s, ".", "")
	trimmed := strings.TrimLeft(digits, "0")
	point += exp - (len(digits) - len(trimmed))
	digits = strings.TrimRight(trimmed, "0")
	if digits == "" {
		return "0", 1, false, nil
	}
	return digits, point, negative, nil
}

// canonicalBudget вычитает размер сериализации v из оставшегося бюджета в байтах.
func canonicalBudget(v any, remaining *int) error {
	spend := func(n int) error {
		if n > *remaining {
			return domain.ErrTooLarge
		}
		*remaining -= n
		return nil
	}
	switch x := v.(type) {
	case nil:
		return spend(4)
	case bool:
		if x {
			return spend(4)
		}
		return spend(5)
	case string:
		if err := spend(2); err != nil {
			return err
		}
		for _, r := range x {
			n := utf8.RuneLen(r)
			switch {
			case r == 0:
				return errors.New("NUL is not supported by PostgreSQL")
			case r == '"' || r == '\\' || r == '\n' || r == '\r' || r == '\t' || r == '\b' || r == '\f':
				n = 2
			case r < 0x20 || r == '<' || r == '>' || r == '&' || r == '\u2028' || r == '\u2029':
				n = 6
			}
			if err := spend(n); err != nil {
				return err
			}
		}
	case json.Number:
		digits, point, negative, err := decimalLayout(x)
		if err != nil {
			return err
		}
		n := len(digits)
		switch {
		case point <= 0:
			n += 2 - point
		case point >= n:
			n = point
		default:
			n++
		}
		if negative {
			n++
		}
		return spend(n)
	case []any:
		n := 2
		if len(x) > 0 {
			n += len(x) - 1
		}
		if err := spend(n); err != nil {
			return err
		}
		for _, item := range x {
			if err := canonicalBudget(item, remaining); err != nil {
				return err
			}
		}
	case map[string]any:
		n := 2
		if len(x) > 0 {
			n += 2*len(x) - 1
		} // двоеточия и запятые
		if err := spend(n); err != nil {
			return err
		}
		for k, item := range x {
			if err := canonicalBudget(k, remaining); err != nil {
				return err
			}
			if err := canonicalBudget(item, remaining); err != nil {
				return err
			}
		}
	}
	return nil
}
