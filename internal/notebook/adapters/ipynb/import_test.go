package ipynb

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
	"github.com/google/uuid"
)

func TestDecodeImport(t *testing.T) {
	input := importDocument(`{"cell_type":"code","source":["я\n","😀"],"metadata":{"cellestial_runtime":{"output_truncated":true}},"extra":42},
		{"cell_type":"raw","id":null,"source":{}},
		{"cell_type":"markdown","id":"keep_1","source":"text","attachments":{"a":{"image/png":"abc"}}}`)
	doc, cells, skipped, err := DecodeImport(input)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 1 || len(cells) != 2 || cells[0].Source != "я\n😀" || cells[1].ID != "keep_1" || !cells[0].OutputTruncated {
		t.Fatalf("unexpected import: skipped=%d cells=%+v", skipped, cells)
	}
	if _, parseErr := uuid.Parse(cells[0].ID); parseErr != nil {
		t.Fatal(parseErr)
	}
	out, err := EncodeDocument(doc, cells)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"outputs":[]`, `"execution_count":null`, `"extra":42`, `"attachments":{"a":{"image/png":"abc"}}`, `"metadata":{"keep":true}`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestDecodeImportStoredResults(t *testing.T) {
	input := importDocument(`{"cell_type":"code","id":"existing","source":"print(1)","outputs":[{"output_type":"stream","text":"1\n"}],"execution_count":3},
		{"cell_type":"markdown","source":"one"},{"cell_type":"markdown","source":"two"}`)
	doc, cells, skipped, err := DecodeImport(input)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 || len(cells) != 3 || cells[0].ID != "existing" || cells[1].ID == cells[2].ID {
		t.Fatal("unexpected cells or raw count")
	}
	out, err := EncodeDocument(doc, cells)
	if err != nil {
		t.Fatal(err)
	}
	_, decoded, err := DecodeDocument(out)
	if err != nil {
		t.Fatal(err)
	}
	if decoded[0].ExecutionCount == nil || *decoded[0].ExecutionCount != 3 ||
		string(decoded[0].Outputs) != `[{"output_type":"stream","text":"1\n"}]` {
		t.Fatal("stored results changed during round-trip")
	}
}

func TestDecodeImportValidation(t *testing.T) {
	for name, raw := range map[string][]byte{
		"missing metadata":           []byte(`{"nbformat":4,"nbformat_minor":5,"cells":[]}`),
		"null minor":                 []byte(`{"nbformat":4,"nbformat_minor":null,"metadata":{},"cells":[]}`),
		"negative minor":             []byte(`{"nbformat":4,"nbformat_minor":-1,"metadata":{},"cells":[]}`),
		"unknown type":               importDocument(`{"cell_type":"other","source":"private text"}`),
		"null source element":        importDocument(`{"cell_type":"code","source":[null]}`),
		"empty id":                   importDocument(`{"cell_type":"code","id":"","source":""}`),
		"null id":                    importDocument(`{"cell_type":"code","id":null,"source":""}`),
		"invalid id":                 importDocument(`{"cell_type":"code","id":"a b","source":""}`),
		"duplicate id":               importDocument(`{"cell_type":"code","id":"a","source":""},{"cell_type":"markdown","id":"a","source":""}`),
		"null metadata":              importDocument(`{"cell_type":"code","source":"","metadata":null}`),
		"null outputs":               importDocument(`{"cell_type":"code","source":"","outputs":null}`),
		"invalid attachments":        importDocument(`{"cell_type":"markdown","source":"","attachments":[]}`),
		"fractional execution count": importDocument(`{"cell_type":"code","source":"","execution_count":1.5}`),
		"raw NUL":                    importDocument(`{"cell_type":"raw","source":"\u0000"}`),
		"surrogate":                  importDocument(`{"cell_type":"code","source":"\uD800"}`),
		"invalid UTF-8":              importDocument("{\"cell_type\":\"raw\",\"source\":\"\xff\"}"),
		"number overflow":            importDocument(`{"cell_type":"raw","extra":1e999999}`),
		"trailing JSON":              append(importDocument(""), []byte(" null")...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := DecodeImport(raw); err == nil || strings.Contains(err.Error(), "private text") {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestDecodeImportLimits(t *testing.T) {
	for _, tc := range []struct {
		cells    string
		skipped  int
		tooLarge bool
	}{
		{"", 0, false},
		{strings.TrimSuffix(strings.Repeat(`{"cell_type":"raw"},`, 1000), ","), 1000, false},
		{strings.TrimSuffix(strings.Repeat(`{"cell_type":"raw"},`, 1001), ","), 0, true},
		{`{"cell_type":"code","source":"` + strings.Repeat("я", 32768) + `"}`, 0, false},
		{`{"cell_type":"code","source":"` + strings.Repeat("я", 32768) + `x"}`, 0, true},
	} {
		_, _, skipped, err := DecodeImport(importDocument(tc.cells))
		if tc.tooLarge {
			if !errors.Is(err, domain.ErrTooLarge) {
				t.Fatalf("expected size error, got %v", err)
			}
		} else if err != nil || skipped != tc.skipped {
			t.Fatalf("skipped=%d, error=%v", skipped, err)
		}
	}
}

// importDocument оборачивает JSON ячеек в минимальный документ nbformat 4.
func importDocument(cells string) []byte {
	return []byte(`{"nbformat":4,"nbformat_minor":0,"metadata":{"keep":true},"cells":[` + cells + `]}`)
}

func TestDecodeImportByteBudget(t *testing.T) {
	t.Run("input including whitespace", func(t *testing.T) {
		input := append(importDocument(""), []byte(strings.Repeat(" ", 1024))...)
		if _, _, _, err := decodeImport(input, len(input)); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := decodeImport(input, len(input)-1); !errors.Is(err, domain.ErrTooLarge) {
			t.Fatalf("expected input size error, got %v", err)
		}
	})
	t.Run("normalized including new fields", func(t *testing.T) {
		input := importDocument(`{"cell_type":"code","source":["<","&"],"extra":1e100}`)
		doc, cells, _, err := DecodeImport(input)
		if err != nil {
			t.Fatal(err)
		}
		out, err := EncodeDocument(doc, cells)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := decodeImport(input, len(out)); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := decodeImport(input, len(out)-1); !errors.Is(err, domain.ErrTooLarge) {
			t.Fatalf("expected normalized size error, got %v", err)
		}
	})
	t.Run("discarded data does not consume normalized budget", func(t *testing.T) {
		input := importDocument(`{"cell_type":"raw","extra":1e1000}`)
		if _, _, skipped, err := decodeImport(input, len(input)); skipped != 1 || err != nil {
			t.Fatalf("skipped=%d, error=%v", skipped, err)
		}
	})
}
