package ipynb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

// CanonicalJSON uses compact encoding/json escaping, sorted object keys, UTF-8,
// and decimal numbers without an exponent or insignificant trailing zeroes.
// Decimal normalization makes accounting independent of jsonb's number spelling.
func CanonicalJSON(raw []byte) (json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("invalid UTF-8")
	}
	// encoding/json replaces unpaired UTF-16 surrogates. Reject them before decoding.
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
	value, err := normalize(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
func normalize(v any) (any, error) {
	switch x := v.(type) {
	case string:
		if strings.ContainsRune(x, 0) {
			return nil, errors.New("NUL is not supported by PostgreSQL")
		}
	case json.Number:
		// JSON decimal denominators have only factors 2 and 5. The input exponent
		// bounds avoid expanding values beyond PostgreSQL numeric's supported scale.
		s := string(x)
		exp := 0
		if i := strings.IndexAny(s, "eE"); i >= 0 {
			var e error
			exp, e = strconv.Atoi(s[i+1:])
			if e != nil || exp > 131071 || exp < -16383 {
				return nil, errors.New("number outside PostgreSQL range")
			}
			s = s[:i]
		}

		integerDigits := len(s)
		if dot := strings.IndexByte(s, '.'); dot >= 0 {
			integerDigits = dot
		}
		if strings.HasPrefix(s, "-") {
			integerDigits--
		}
		if integerDigits+exp > 131072 {
			return nil, errors.New("number outside PostgreSQL range")
		}
		scale := 0
		if i := strings.IndexByte(s, '.'); i >= 0 {
			scale = len(s) - i - 1
		}
		scale -= exp
		if scale < 0 {
			scale = 0
		}
		if scale > 16383 {
			return nil, errors.New("number outside PostgreSQL range")
		}
		r, ok := new(big.Rat).SetString(string(x))
		if !ok {
			return nil, errors.New("invalid JSON number")
		}
		s = r.FloatString(scale)
		if strings.Contains(s, ".") {
			s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		}
		if s == "-0" {
			s = "0"
		}
		if len(s) > 131073 {
			return nil, errors.New("number outside PostgreSQL range")
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
func object(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("expected JSON object")
	}
	return m, nil
}
func take(m map[string]json.RawMessage, k string) json.RawMessage { v := m[k]; delete(m, k); return v }

// DecodeDocument is used only at import/migration boundaries.
func DecodeDocument(raw []byte) (domain.Document, []domain.Cell, error) {
	var doc domain.Document
	normalized, err := CanonicalJSON(raw)
	if err != nil {
		return doc, nil, err
	}
	m, err := object(normalized)
	if err != nil {
		return doc, nil, err
	}
	if err = json.Unmarshal(take(m, "nbformat"), &doc.NBFormat); err != nil || doc.NBFormat != 4 {
		return doc, nil, errors.New("expected nbformat 4")
	}
	if minor := take(m, "nbformat_minor"); len(minor) > 0 {
		if err = json.Unmarshal(minor, &doc.NBFormatMinor); err != nil {
			return doc, nil, err
		}
	}
	doc.Metadata = take(m, "metadata")
	if len(doc.Metadata) > 0 {
		if _, err = object(doc.Metadata); err != nil {
			return doc, nil, err
		}
	}
	var originals []json.RawMessage
	cellsRaw := take(m, "cells")
	if string(cellsRaw) == "null" {
		return doc, nil, errors.New("cells must be an array")
	}
	if err = json.Unmarshal(cellsRaw, &originals); err != nil {
		return doc, nil, err
	}
	doc.Extra = m
	if len(originals) > 1000 {
		return doc, nil, domain.ErrTooLarge
	}
	cells := make([]domain.Cell, 0, len(originals))
	seen := map[string]bool{}
	for _, rawCell := range originals {
		cm, e := object(rawCell)
		if e != nil {
			return doc, nil, e
		}
		var c domain.Cell
		if e = json.Unmarshal(take(cm, "id"), &c.ID); e != nil || c.ID == "" || seen[c.ID] {
			return doc, nil, errors.New("invalid or duplicate cell id")
		}
		seen[c.ID] = true
		if e = json.Unmarshal(take(cm, "cell_type"), &c.Kind); e != nil {
			return doc, nil, e
		}
		if c.Kind != domain.CellKindCode && c.Kind != domain.CellKindMarkdown && c.Kind != "raw" {
			return doc, nil, ErrUnknownCellType
		}
		var src source
		sourceRaw := take(cm, "source")
		if string(sourceRaw) == "null" {
			return doc, nil, errors.New("source must be string or array")
		}
		if e = json.Unmarshal(sourceRaw, &src); e != nil {
			return doc, nil, e
		}
		c.Source = string(src)
		if len(c.Source) > 64*1024 {
			return doc, nil, domain.ErrTooLarge
		}
		c.Metadata = take(cm, "metadata")
		if len(c.Metadata) > 0 {
			if _, e = object(c.Metadata); e != nil {
				return doc, nil, e
			}
		}
		if e = splitRuntimeMetadata(&c); e != nil {
			return doc, nil, e
		}
		c.Outputs = take(cm, "outputs")
		if len(c.Outputs) > 0 {
			if e = ValidateOutputs(c.Outputs); e != nil {
				return doc, nil, e
			}
		}
		ec := take(cm, "execution_count")
		c.HasExecutionCount = len(ec) > 0
		if len(ec) > 0 {
			if e = json.Unmarshal(ec, &c.ExecutionCount); e != nil {
				return doc, nil, e
			}
		}
		c.Attachments = take(cm, "attachments")
		c.Extra = cm
		cells = append(cells, c)
	}
	sizes := make([]int64, len(cells))
	for i, c := range cells {
		sizes[i] = CellSize(c)
	}
	if DocumentSize(HeaderSize(doc), sizes) > 128*1024*1024 {
		return doc, nil, domain.ErrTooLarge
	}
	return doc, cells, nil
}
func copyFields(m map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(m)+5)
	for k, v := range m {
		out[k] = v
	}
	return out
}
func put(m map[string]json.RawMessage, k string, v any) { m[k], _ = json.Marshal(v) }
func header(doc domain.Document) map[string]json.RawMessage {
	m := copyFields(doc.Extra)
	delete(m, "cells")
	put(m, "nbformat", doc.NBFormat)
	put(m, "nbformat_minor", doc.NBFormatMinor)
	if len(doc.Metadata) > 0 {
		m["metadata"] = doc.Metadata
	}
	return m
}
func light(c domain.Cell) map[string]json.RawMessage {
	m := copyFields(c.Extra)
	for _, k := range []string{"outputs", "execution_count", "attachments"} {
		delete(m, k)
	}
	put(m, "id", c.ID)
	put(m, "cell_type", c.Kind)
	put(m, "source", c.Source)
	if len(c.Metadata) > 0 {
		m["metadata"] = c.Metadata
	}
	return m
}

// NormalizeFields canonicalizes stored JSON fields without loading other cells.
func NormalizeFields(doc *domain.Document, cells []domain.Cell) error {
	raw, e := json.Marshal(header(*doc))
	if e != nil {
		return e
	}
	raw, e = CanonicalJSON(raw)
	if e != nil {
		return e
	}
	m, e := object(raw)
	if e != nil {
		return e
	}
	doc.Metadata = take(m, "metadata")
	delete(m, "nbformat")
	delete(m, "nbformat_minor")
	doc.Extra = m
	for i := range cells {
		c := &cells[i]
		if e = splitRuntimeMetadata(c); e != nil {
			return e
		}
		raw, e = json.Marshal(light(*c))
		if e != nil {
			return e
		}
		raw, e = CanonicalJSON(raw)
		if e != nil {
			return e
		}
		m, e = object(raw)
		if e != nil {
			return e
		}
		c.Metadata = take(m, "metadata")
		for _, k := range []string{"id", "cell_type", "source"} {
			delete(m, k)
		}
		c.Extra = m
		for _, p := range []*json.RawMessage{&c.Outputs, &c.Attachments} {
			if len(*p) > 0 {
				*p, e = CanonicalJSON(*p)
				if e != nil {
					return e
				}
			}
		}
		if len(c.Outputs) > 0 {
			if e = ValidateOutputs(c.Outputs); e != nil {
				return e
			}
		}

	}
	return nil
}
func HeaderSize(doc domain.Document) int64 { b, _ := json.Marshal(header(doc)); return int64(len(b)) }
func LightSize(c domain.Cell) int64        { b, _ := json.Marshal(light(c)); return int64(len(b)) }
func DataSizes(c domain.Cell) (outputs, execution, attachments int64) {
	if len(c.Outputs) > 0 {
		outputs = int64(len(`,"outputs":`) + len(c.Outputs))
	}
	if c.HasExecutionCount {
		b, _ := json.Marshal(c.ExecutionCount)
		execution = int64(len(`,"execution_count":`) + len(b))
	}
	if len(c.Attachments) > 0 {
		attachments = int64(len(`,"attachments":`) + len(c.Attachments))
	}
	return outputs, execution, attachments
}
func CellSize(c domain.Cell) int64 {
	o, e, a := DataSizes(c)
	return LightSize(c) + o + e + a + RuntimeMetadataSize(c)
}
func DocumentSize(headerBytes int64, cellBytes []int64) int64 {
	size := headerBytes + int64(len(`,"cells":[]`))
	for _, b := range cellBytes {
		size += b
	}
	if len(cellBytes) > 0 {
		size += int64(len(cellBytes) - 1)
	}
	return size
}

// EncodeDocument constructs a full document only at the export boundary.
func EncodeDocument(doc domain.Document, cells []domain.Cell) ([]byte, error) {
	m := header(doc)
	out := make([]map[string]json.RawMessage, 0, len(cells))
	for _, c := range cells {
		if (c.HasRuntimeMetadata || c.OutputTruncated) && RuntimeMetadataOverhead(c) < 0 {
			return nil, errors.New("runtime metadata namespace is occupied")
		}
		cm := runtimeMetadata(c, light(c))
		if len(c.Outputs) > 0 {
			cm["outputs"] = c.Outputs
		}
		if c.HasExecutionCount {
			put(cm, "execution_count", c.ExecutionCount)
		}
		if len(c.Attachments) > 0 {
			cm["attachments"] = c.Attachments
		}
		out = append(out, cm)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode cells: %w", err)
	}
	m["cells"] = b
	return json.Marshal(m)
}

// ValidateOutputs enforces the shared import/runtime/content representation.
// Detailed nbformat output validation belongs to the producer, but every item
// must be a JSON object so persisted documents remain readable through content.
func ValidateOutputs(raw json.RawMessage) error {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	if items == nil {
		return errors.New("outputs must be an array")
	}
	for _, item := range items {
		if _, err := object(item); err != nil {
			return errors.New("each output must be an object")
		}
	}
	return nil
}

// splitRuntimeMetadata separates the owned result flag from arbitrary metadata.
// The original namespace and all its other fields remain in the lightweight row.
func splitRuntimeMetadata(c *domain.Cell) error {
	if len(c.Metadata) == 0 {
		return nil
	}
	fields, err := object(c.Metadata)
	if err != nil {
		return err
	}
	raw, present := fields["cellestial_runtime"]
	if !present {
		return nil
	}
	marker, err := object(raw)
	if err != nil {
		return nil // Preserve unrelated metadata; projection detects a namespace collision.
	}
	flag, present := marker["output_truncated"]
	if !present {
		return nil
	}
	if string(flag) != "true" && string(flag) != "false" {
		return nil // Preserve an unknown non-runtime value without coercion.
	}
	c.OutputTruncated = string(flag) == "true"
	c.HasRuntimeMetadata = true
	delete(marker, "output_truncated")
	fields["cellestial_runtime"], _ = json.Marshal(marker)
	c.Metadata, _ = json.Marshal(fields)
	return nil
}
func runtimeMetadata(c domain.Cell, fields map[string]json.RawMessage) map[string]json.RawMessage {
	if !c.HasRuntimeMetadata && !c.OutputTruncated {
		return fields
	}
	metadata := map[string]json.RawMessage{}
	if len(c.Metadata) > 0 {
		_ = json.Unmarshal(c.Metadata, &metadata)
	}
	marker := map[string]json.RawMessage{}
	if raw := metadata["cellestial_runtime"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &marker)
	}
	put(marker, "output_truncated", c.OutputTruncated)
	metadata["cellestial_runtime"], _ = json.Marshal(marker)
	fields["metadata"], _ = json.Marshal(metadata)
	return fields
}

// RuntimeMetadataOverhead is stored once so a flag change needs no metadata read.
func RuntimeMetadataOverhead(c domain.Cell) int64 {
	if len(c.Metadata) > 0 {
		var metadata map[string]json.RawMessage
		_ = json.Unmarshal(c.Metadata, &metadata)
		if raw, present := metadata["cellestial_runtime"]; present {
			marker, err := object(raw)
			if err != nil {
				return -1
			}
			if flag, present := marker["output_truncated"]; present && string(flag) != "true" && string(flag) != "false" {
				return -1
			}
		}
	}
	c.HasRuntimeMetadata = true
	c.OutputTruncated = false
	raw, _ := json.Marshal(runtimeMetadata(c, light(c)))
	return int64(len(raw)) - LightSize(c) - 5
}
func RuntimeMetadataSize(c domain.Cell) int64 {
	if !c.HasRuntimeMetadata && !c.OutputTruncated {
		return 0
	}
	size := RuntimeMetadataOverhead(c) + 5
	if c.OutputTruncated {
		size--
	}
	return size
}
