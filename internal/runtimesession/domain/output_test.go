package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestOutputClearAndLateDisplayUpdate проверяет очистку вывода и обновление ранее показанного результата по
// display_id.
func TestOutputClearAndLateDisplayUpdate(t *testing.T) {
	e := Execution{Outputs: []json.RawMessage{}}
	for _, ev := range []AgentEvent{{Kind: "kernel_message", MsgType: "stream", Content: json.RawMessage(`{"name":"stdout","text":"old"}`)}, {Kind: "kernel_message", MsgType: "clear_output", Content: json.RawMessage(`{"wait":true}`)}} {
		if err := e.Apply(ev); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.Outputs) != 1 {
		t.Fatal("wait cleared before output")
	}
	_ = e.Apply(AgentEvent{Kind: "kernel_message", MsgType: "display_data", Content: json.RawMessage(`{"data":{"text/plain":"new"},"metadata":{},"transient":{"display_id":"d"}}`)})
	if len(e.Outputs) != 1 || strings.Contains(string(e.Outputs[0]), "old") {
		t.Fatal(string(e.Outputs[0]))
	}
	_ = e.Apply(AgentEvent{Kind: "kernel_message", MsgType: "update_display_data", Content: json.RawMessage(`{"data":{"text/plain":"updated"},"metadata":{},"transient":{"display_id":"d"}}`)})
	if !strings.Contains(string(e.Outputs[0]), "updated") {
		t.Fatal(string(e.Outputs[0]))
	}
}

// TestExecutionCountSchedulesProjectionWithoutOutput проверяет сохранение счётчика выполнения даже при
// отсутствии обычного вывода.
func TestExecutionCountSchedulesProjectionWithoutOutput(t *testing.T) {
	x := Execution{Projection: "applied"}
	if e := x.Apply(AgentEvent{Kind: "kernel_message", MsgType: "execute_input", Content: json.RawMessage(`{"execution_count":4}`)}); e != nil {
		t.Fatal(e)
	}
	if x.ExecutionCount == nil || *x.ExecutionCount != 4 || x.Projection != "pending" {
		t.Fatal(x.ExecutionCount, x.Projection)
	}
}
