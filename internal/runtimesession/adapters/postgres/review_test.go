package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
)

// TestOutputWithNULSurvivesArchive проверяет сохранность NUL и буквальной escape-последовательности в журнале и результате.
func TestOutputWithNULSurvivesArchive(t *testing.T) {
	s, _, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, err := s.Start(ctx, newSession(owner, nb))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("print(42)"))
	_, err = s.Admit(ctx, owner, v.ID, D.ExecuteRequest{ExecutionID: "nul", CellID: "cell", SourceHash: hex.EncodeToString(sum[:]), TimeoutSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.Claim(ctx, "nul-worker")
	if err != nil {
		t.Fatal(err)
	}
	text := "before\x00after \\u0000"
	content, _ := json.Marshal(map[string]string{"name": "stdout", "text": text})
	result := json.RawMessage(`{"value":"\u0000"}`)
	events := []D.AgentEvent{{Seq: 1, SessionID: v.ID, ExecutionID: "nul", Kind: "kernel_message", MsgType: "stream", Content: content}, {Seq: 2, SessionID: v.ID, ExecutionID: "nul", Kind: "execution_state", State: "SUCCEEDED", Result: result}}
	if err = s.CommitEvents(ctx, l, events); err != nil {
		t.Fatal("archive NUL:", err)
	}
	x, err := s.Execution(ctx, owner, v.ID, "nul")
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Text string `json:"text"`
	}
	if len(x.Outputs) != 1 {
		t.Fatal(x)
	}
	if err = json.Unmarshal(x.Outputs[0], &output); err != nil || output.Text != text {
		t.Fatal(output, err)
	}
	var value struct {
		Value string `json:"value"`
	}
	if err = json.Unmarshal(x.Result, &value); err != nil || value.Value != "\x00" {
		t.Fatal(value, err)
	}
	page, err := s.Events(ctx, owner, v.ID, 0, D.EventLimits{})
	if err != nil || len(page.Events) != 2 {
		t.Fatal(page, err)
	}
	var event D.AgentEvent
	if err = json.Unmarshal(page.Events[0].Body, &event); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(event.Content, &output); err != nil || output.Text != text {
		t.Fatal(output, err)
	}
	if err = s.Project(ctx, &projectionWriter{}); err != nil {
		t.Fatal(err)
	}
}

// TestEventIgnoresUnrelatedHistory проверяет, что повреждённый старый результат не читается при событии другой команды.
func TestEventIgnoresUnrelatedHistory(t *testing.T) {
	s, p, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, err := s.Start(ctx, newSession(owner, nb))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("print(42)"))
	for _, id := range []string{"history", "current"} {
		if _, err = s.Admit(ctx, owner, v.ID, D.ExecuteRequest{ExecutionID: id, CellID: id, SourceHash: hex.EncodeToString(sum[:]), TimeoutSeconds: 300}); err != nil {
			t.Fatal(err)
		}
	}
	// Чужой архив намеренно не декодируется в Execution: фоновый путь не должен его загружать.
	if _, err = p.Exec(ctx, `UPDATE runtime_executions SET data='{"Public":{"state":"SUCCEEDED","outputs":42},"Dispatch":"ACCEPTED"}' WHERE id='history'`); err != nil {
		t.Fatal(err)
	}
	l, err := s.Claim(ctx, "worker")
	if err != nil {
		t.Fatal(err)
	}
	ev := D.AgentEvent{Seq: 1, SessionID: v.ID, ExecutionID: "current", Kind: "kernel_message", MsgType: "stream", Content: json.RawMessage(`{"name":"stdout","text":"ok"}`)}
	if err = s.CommitEvents(ctx, l, []D.AgentEvent{ev}); err != nil {
		t.Fatal("unrelated history was loaded:", err)
	}
	x, err := s.Execution(ctx, owner, v.ID, "current")
	if err != nil || len(x.Outputs) != 1 {
		t.Fatal(x, err)
	}
	q, err := s.Queue(ctx, owner, v.ID)
	if err != nil || len(q.Items) != 1 || q.Items[0].ExecutionID != "current" {
		t.Fatal(q, err)
	}
}

// TestExecutionEncodingKeepsNUL проверяет обратимость хранения исходника, вывода и инспекции без запрещённого JSONB escape.
func TestExecutionEncodingKeepsNUL(t *testing.T) {
	x := D.Execution{Code: "a\x00b", Outputs: []json.RawMessage{json.RawMessage(`{"text":"\u0000 \\u0000"}`)}, Result: json.RawMessage(`{"variables":[{"name":"x","preview":"\u0000"}]}`)}
	b, err := encodeExecution(x)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `\u0000`) {
		t.Fatalf("JSONB still contains NUL escape: %s", b)
	}
	y, err := decodeExecution(b)
	if err != nil || y.Code != x.Code || string(y.Outputs[0]) != string(x.Outputs[0]) || string(y.Result) != string(x.Result) {
		t.Fatal(y, err)
	}
}

// TestPendingSelectionIgnoresHistory проверяет выбор только следующей ожидающей команды и сохранность завершённой истории.
func TestPendingSelectionIgnoresHistory(t *testing.T) {
	s, p, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, err := s.Start(ctx, newSession(owner, nb))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("print(42)"))
	for _, id := range []string{"history", "first", "second"} {
		if _, err = s.Admit(ctx, owner, v.ID, D.ExecuteRequest{ExecutionID: id, CellID: id, SourceHash: hex.EncodeToString(sum[:]), TimeoutSeconds: 300}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = p.Exec(ctx, `UPDATE runtime_executions SET data='{"Public":{"state":"SUCCEEDED","outputs":42},"Dispatch":"ACCEPTED"}' WHERE id='history'`); err != nil {
		t.Fatal(err)
	}
	x, err := s.NextPending(ctx, v.ID)
	if err != nil || x.ID != "first" {
		t.Fatal(x, err)
	}
	l, err := s.Claim(ctx, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MarkUnknown(ctx, l); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		x, err = s.Execution(ctx, owner, v.ID, id)
		if err != nil || x.State != "UNKNOWN" {
			t.Fatal(x, err)
		}
	}
	if _, err = s.NextPending(ctx, v.ID); !errors.Is(err, D.ErrNotFound) {
		t.Fatal("no pending command expected", err)
	}
	var state string
	if err = p.QueryRow(ctx, `SELECT data->'Public'->>'state' FROM runtime_executions WHERE id='history'`).Scan(&state); err != nil || state != "SUCCEEDED" {
		t.Fatal(state, err)
	}
}

// TestDisplayUpdatePagesOnlyMatchingGeneration проверяет обновление общего display_id через границу страницы без чужих поколений и выводов.
func TestDisplayUpdatePagesOnlyMatchingGeneration(t *testing.T) {
	s, p, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, err := s.Start(ctx, newSession(owner, nb))
	if err != nil {
		t.Fatal(err)
	}
	displayID := "display\x00id"
	for i := 0; i < 105; i++ {
		generation, id := "current", displayID
		if i == 103 {
			generation = "old"
		}
		if i == 104 {
			id = "other"
		}
		output, _ := json.Marshal(map[string]any{"output_type": "display_data", "data": map[string]string{"text/plain": "before"}, "transient": map[string]string{"display_id": id}})
		x := D.Execution{SessionID: v.ID, ID: fmt.Sprintf("run-%03d", i), NotebookID: nb, OwnerID: owner, CellID: fmt.Sprintf("cell-%03d", i), Attempt: 1, State: "SUCCEEDED", Dispatch: "ACCEPTED", Generation: generation, Outputs: []json.RawMessage{output}, Projection: "applied"}
		raw, e := encodeExecution(x)
		if e != nil {
			t.Fatal(e)
		}
		if _, err = p.Exec(ctx, "INSERT INTO runtime_executions(session_id,id,notebook_id,cell_id,attempt,data) VALUES($1,$2,$3,$4,1,$5)", v.ID, x.ID, nb, x.CellID, raw); err != nil {
			t.Fatal(err)
		}
	}
	l, err := s.Claim(ctx, "display-worker")
	if err != nil {
		t.Fatal(err)
	}
	content, _ := json.Marshal(map[string]any{"data": map[string]string{"text/plain": "after"}, "transient": map[string]string{"display_id": displayID}})
	if err = s.CommitEvents(ctx, l, []D.AgentEvent{{Seq: 1, SessionID: v.ID, ExecutionID: "background", Generation: "current", Kind: "kernel_message", MsgType: "update_display_data", Content: content}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		x, e := s.Execution(ctx, owner, v.ID, fmt.Sprintf("run-%03d", i))
		if e != nil {
			t.Fatal(e)
		}
		want := "after"
		if i >= 103 {
			want = "before"
		}
		if len(x.Outputs) != 1 || !strings.Contains(string(x.Outputs[0]), want) {
			t.Fatalf("run %d wrong display update: %s", i, x.Outputs)
		}
	}
}
