package ipynb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
)

// runtimeMemoryFiles заменяет S3 обычной картой файлов для тестов сохранения результата.
type runtimeMemoryFiles map[string][]byte

// Get читает содержимое файла из памяти вместо обращения к S3.
func (f runtimeMemoryFiles) Get(_ context.Context, k string) ([]byte, error) { return f[k], nil }

// Put сохраняет содержимое файла в памяти вместо обращения к S3.
func (f runtimeMemoryFiles) Put(_ context.Context, k string, b []byte) error { f[k] = b; return nil }

// TestRuntimeOutputPreservesUnknownFieldsAndOriginalRevision проверяет сохранность неизвестных полей и
// старого S3-объекта при публикации результата.
func TestRuntimeOutputPreservesUnknownFieldsAndOriginalRevision(t *testing.T) {
	raw := []byte(`{"cells":[{"id":"c","cell_type":"code","source":["x"],"outputs":[],"metadata":{"keep":true},"extra":42}],"metadata":{"custom":true},"root_extra":7}`)
	f := runtimeMemoryFiles{"old": raw}
	sum := sha256.Sum256([]byte("x"))
	count := int64(2)
	x := D.Execution{ID: "run", CellID: "c", SourceHash: hex.EncodeToString(sum[:]), ExecutionCount: &count, Outputs: []json.RawMessage{json.RawMessage(`{"output_type":"display_data","data":{"text/plain":"42"},"metadata":{},"transient":{"display_id":"d"}}`)}}
	key, ok, err := (RuntimeFiles{Files: f}).ApplyOutput(context.Background(), "old", x)
	if err != nil || !ok || key == "old" {
		t.Fatalf("%s %v %v", key, ok, err)
	}
	if string(f["old"]) != string(raw) {
		t.Fatal("original immutable revision overwritten")
	}
	for _, v := range []string{`"root_extra":7`, `"extra":42`, `"keep":true`, `"source":["x"]`, `"execution_count":2`, `"text/plain":"42"`} {
		if !strings.Contains(string(f[key]), v) {
			t.Fatalf("missing %s in %s", v, f[key])
		}
	}
	if strings.Contains(string(f[key]), "transient") {
		t.Fatal("transport-only display metadata written into ipynb")
	}
	cells, err := Parse(f[key])
	if err != nil {
		t.Fatal(err)
	}
	cells = append(cells, cells[0])
	cells[1].ID = "new"
	rewritten, err := (Codec{}).Rewrite(f[key], cells)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rewritten), `"text/plain":"42"`) {
		t.Fatal("editor erased output")
	}
	x.SourceHash = strings.Repeat("a", 64)
	_, ok, err = (RuntimeFiles{Files: f}).ApplyOutput(context.Background(), key, x)
	if err != nil || ok {
		t.Fatal("stale source accepted", err)
	}
}

// TestParseReadsPersistedOutputAndTruncation проверяет чтение сохранённых выводов, счётчика и признака
// усечения из ipynb.
func TestParseReadsPersistedOutputAndTruncation(t *testing.T) {
	raw := []byte(`{"cells":[{"id":"a","cell_type":"code","source":"print(1)","outputs":[{"output_type":"stream","name":"stdout","text":"1"}],"execution_count":3,"metadata":{"cellestial_runtime":{"output_truncated":true}}}]}`)
	cells, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	result := cells[0].Result
	if result == nil || len(result.Outputs) != 1 || result.ExecutionCount == nil || *result.ExecutionCount != 3 || !result.Truncated {
		t.Fatal(result)
	}
}
