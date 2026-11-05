package domain

import (
	"encoding/json"
	"testing"
)

// TestInspectionDoesNotProjectKernelMessagesIntoCell проверяет, что служебные сообщения инспекции не
// становятся выводом ячейки.
func TestInspectionDoesNotProjectKernelMessagesIntoCell(t *testing.T) {
	x := Execution{ID: "inspect-1", Kind: "inspect", Projection: "none"}
	if err := x.Apply(AgentEvent{Kind: "kernel_message", MsgType: "stream", Content: json.RawMessage(`{"name":"stdout","text":"private inspection transport"}`)}); err != nil {
		t.Fatal(err)
	}
	if len(x.Outputs) != 0 || x.Projection != "none" {
		t.Fatal("inspection leaked to a cell", x)
	}
	if err := x.Apply(AgentEvent{Kind: "execution_state", State: "SUCCEEDED", Result: json.RawMessage(`{"variables":[{"name":"x","type":"int","preview":"42"}]}`)}); err != nil {
		t.Fatal(err)
	}
	v, err := x.Inspection()
	if err != nil || len(v.Variables) != 1 || v.Variables[0].Name != "x" {
		t.Fatal(v, err)
	}
}
