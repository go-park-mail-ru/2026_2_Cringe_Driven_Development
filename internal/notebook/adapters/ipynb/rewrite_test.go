package ipynb

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/domain"
)

const richNotebook = `{"nbformat":4,"nbformat_minor":5,"metadata":{"kernelspec":{"name":"python3"}},"custom":{"keep":true},"cells":[{"id":"a","cell_type":"code","metadata":{"tags":["keep"]},"source":["print(1)","\n"],"outputs":[{"output_type":"stream","name":"stdout","text":"1\n"}],"execution_count":7,"custom_cell":42},{"id":"b","cell_type":"markdown","metadata":{"collapsed":true},"source":"# Title","attachments":{"x.png":{"image/png":"data"}}}]}`

func TestRewritePreservesDocumentAndCellFields(t *testing.T) {
	cells, err := Parse([]byte(richNotebook))
	if err != nil {
		t.Fatal(err)
	}
	cells[0], cells[1] = cells[1], cells[0]
	data, err := (Codec{}).Rewrite([]byte(richNotebook), cells)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Metadata json.RawMessage              `json:"metadata"`
		Custom   json.RawMessage              `json:"custom"`
		Cells    []map[string]json.RawMessage `json:"cells"`
	}
	if err = json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got.Metadata), "python3") || string(got.Custom) != `{"keep":true}` {
		t.Fatalf("lost document fields: %s", data)
	}
	if string(got.Cells[0]["id"]) != `"b"` || !strings.Contains(string(got.Cells[0]["attachments"]), "image/png") {
		t.Fatalf("lost moved markdown: %s", data)
	}
	if string(got.Cells[1]["execution_count"]) != "7" || !strings.Contains(string(got.Cells[1]["outputs"]), "stdout") || string(got.Cells[1]["custom_cell"]) != "42" {
		t.Fatalf("lost code fields: %s", data)
	}
	if string(got.Cells[1]["source"]) != `["print(1)","\n"]` {
		t.Fatalf("unchanged source rewritten: %s", data)
	}
}

func TestRewriteClearsOnlyChangedCodeOutput(t *testing.T) {
	cells, err := Parse([]byte(richNotebook))
	if err != nil {
		t.Fatal(err)
	}
	cells[0].Source = "print(2)"
	data, err := (Codec{}).Rewrite([]byte(richNotebook), cells)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Cells []map[string]json.RawMessage `json:"cells"`
	}
	if err = json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if string(got.Cells[0]["outputs"]) != "[]" || string(got.Cells[0]["execution_count"]) != "null" || !strings.Contains(string(got.Cells[0]["metadata"]), "keep") {
		t.Fatalf("invalid code edit: %s", data)
	}
	if !strings.Contains(string(got.Cells[1]["attachments"]), "image/png") {
		t.Fatalf("lost unrelated attachments: %s", data)
	}
}

func TestRewriteAddsAndDeletesCells(t *testing.T) {
	cells := []domain.Cell{{ID: "new", Kind: domain.CellKindCode, Source: "2+2"}}
	data, err := (Codec{}).Rewrite([]byte(richNotebook), cells)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != cells[0] || !strings.Contains(string(data), `"outputs":[]`) {
		t.Fatalf("invalid new cell: %s", data)
	}
	data, err = (Codec{}).Rewrite(data, []domain.Cell{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"cells":[]`) {
		t.Fatalf("want empty cells: %s", data)
	}
}
