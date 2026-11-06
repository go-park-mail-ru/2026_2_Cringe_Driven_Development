package ipynb

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

func TestDocumentRoundTripAndAccounting(t *testing.T) {
	raw := []byte(`{"nbformat":4,"nbformat_minor":5,"metadata":{"a":1e2},"custom":{"x":"😀"},"cells":[{"id":"c","cell_type":"code","source":["  я\n","😀\"\\"],"metadata":{"k":true},"custom":42,"outputs":[{"text":"large"}],"execution_count":3,"attachments":{"x":{"image/png":"abc"}}}]}`)
	doc, cells, err := DecodeDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := EncodeDocument(doc, cells)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err = json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"source":"  я\n😀\"\\"`)) || !bytes.Contains(out, []byte(`"custom":42`)) {
		t.Fatalf("lost fields: %s", out)
	}
	if int64(len(out)) != DocumentSize(HeaderSize(doc), []int64{CellSize(cells[0])}) {
		t.Fatalf("size mismatch: %d", len(out))
	}
	again, cc, err := DecodeDocument(out)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := EncodeDocument(again, cc)
	if err != nil || !bytes.Equal(out, out2) {
		t.Fatalf("unstable export: %s %v", out2, err)
	}
}

func TestDocumentRejectsUnrepresentableText(t *testing.T) {
	for _, raw := range []string{
		`{"nbformat":4,"cells":[{"id":"c","cell_type":"code","source":"\u0000"}]}`,
		`{"nbformat":4,"metadata":{"x":"\u0000"},"cells":[]}`,
		`{"nbformat":4,"metadata":{"x":"\ud800"},"cells":[]}`,
	} {
		if _, _, err := DecodeDocument([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestDocumentRejectsOutputsThatContentCannotRepresent(t *testing.T) {
	for _, outputs := range []string{`[42]`, `["x"]`, `[null]`, `null`} {
		input := []byte(`{"nbformat":4,"nbformat_minor":5,"cells":[{"id":"a","cell_type":"code","source":"","outputs":` + outputs + `}]}`)
		if _, _, err := DecodeDocument(input); err == nil {
			t.Errorf("accepted outputs=%s", outputs)
		}
	}
}

func TestDocumentDoesNotSilentlyDropNullSourceParts(t *testing.T) {
	input := []byte(`{"nbformat":4,"nbformat_minor":5,"cells":[{"id":"a","cell_type":"code","source":["a",null,"b"]}]}`)
	if _, _, err := DecodeDocument(input); err == nil {
		t.Fatal("null source part silently became empty text")
	}
}

func TestRuntimeTruncationFlagSurvivesExportAndReset(t *testing.T) {
	for _, metadata := range []string{`{}`, `{"cellestial_runtime":{"output_truncated":true,"custom":"keep"}}`} {
		doc, cells, err := DecodeDocument([]byte(`{"nbformat":4,"nbformat_minor":5,"metadata":{},"cells":[{"id":"a","cell_type":"code","source":"x","metadata":` + metadata + `,"outputs":[]}]}`))
		if err != nil {
			t.Fatal(err)
		}
		for _, truncated := range []bool{true, false} {
			cells[0].OutputTruncated = truncated
			raw, encodeErr := EncodeDocument(doc, cells)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			_, again, decodeErr := DecodeDocument(raw)
			if decodeErr != nil || again[0].OutputTruncated != truncated {
				t.Fatalf("flag=%v export=%s err=%v", truncated, raw, decodeErr)
			}
			if int64(len(raw)) != DocumentSize(HeaderSize(doc), []int64{CellSize(cells[0])}) {
				t.Fatal("runtime flag size mismatch")
			}
		}
	}
}

func TestUnknownRuntimeNamespaceIsNotLost(t *testing.T) {
	input := []byte(`{"nbformat":4,"nbformat_minor":5,"cells":[{"id":"a","cell_type":"code","source":"x","metadata":{"cellestial_runtime":"custom value"}}]}`)
	doc, cells, err := DecodeDocument(input)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeDocument(doc, cells)
	if err != nil || !bytes.Contains(encoded, []byte(`"cellestial_runtime":"custom value"`)) {
		t.Fatalf("lost unknown metadata: %s %v", encoded, err)
	}
	cells[0].OutputTruncated = true
	if _, err = EncodeDocument(doc, cells); err == nil {
		t.Fatal("runtime flag silently overwrote unknown metadata")
	}
}

func TestCanonicalJSONBudget(t *testing.T) {
	for _, input := range []string{`[1e308,1e308]`, `[1e-308,1e-308]`, `{"nested":[1e308],"escaped":"<😀\\\""}`} {
		out, err := CanonicalJSONLimit([]byte(input), 1024)
		if err != nil {
			t.Fatal(err)
		}
		exact, err := CanonicalJSONLimit([]byte(input), len(out))
		if err != nil || !bytes.Equal(out, exact) {
			t.Fatalf("exact boundary: %v", err)
		}
		if _, err = CanonicalJSONLimit([]byte(input), len(out)-1); !errors.Is(err, domain.ErrTooLarge) {
			t.Fatalf("accepted over budget: %v", err)
		}
	}
}

func TestCanonicalJSONExponentArrayBudget(t *testing.T) {
	input := []byte(`[` + strings.Repeat(`1e308,`, 9999) + `1e308]`)
	if _, err := CanonicalJSONLimit(input, 1024); !errors.Is(err, domain.ErrTooLarge) {
		t.Fatalf("expanded array accepted: %v", err)
	}
}

func TestCanonicalDecimalSpelling(t *testing.T) {
	for input, want := range map[string]string{
		"1e3": "1000", "1e-3": "0.001", "-0.000e3": "0", "120.00e-1": "12", "0.01200e1": "0.12", "-12.30e-2": "-0.123", "1000e-3": "1",
	} {
		got, err := CanonicalJSON([]byte(input))
		if err != nil || string(got) != want {
			t.Errorf("%s: got %s, want %s (%v)", input, got, want, err)
		}
	}
}
