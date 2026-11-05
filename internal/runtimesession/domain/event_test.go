package domain

import (
	"encoding/json"
	"testing"
)

// TestAgentEventPreservesJupyterTransportFields проверяет сохранность полей транспорта Jupyter при
// сериализации события.
func TestAgentEventPreservesJupyterTransportFields(t *testing.T) {
	raw := []byte(`{"seq":12,"kind":"kernel_message","session_id":"11111111-1111-4111-8111-111111111111","execution_id":"run","channel":"iopub","msg_type":"stream","content":{"text":"42"},"metadata":{"keep":true},"parent_msg_id":"parent","buffers":["YWJj"],"timestamp":1791633600}`)
	var ev AgentEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(out, &fields); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"channel", "parent_msg_id", "buffers", "timestamp"} {
		if _, ok := fields[k]; !ok {
			t.Fatal("lost Jupyter field", k)
		}
	}
}
