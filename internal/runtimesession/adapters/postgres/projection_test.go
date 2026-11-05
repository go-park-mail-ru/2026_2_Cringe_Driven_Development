package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
)

// projectionWriter имитирует успешную или ошибочную запись результата в файловое хранилище.
type projectionWriter struct {
	// fail Управляемый отказ записи результата в тесте.
	fail bool
	// calls Число вызовов зависимости в тесте.
	calls int
}

// ApplyOutput имитирует запись результата в файл, включая управляемый отказ для проверки повтора.
func (w *projectionWriter) ApplyOutput(_ context.Context, _ string, _ D.Execution) (string, bool, error) {
	w.calls++
	if w.fail {
		return "", false, errors.New("S3 unavailable")
	}
	return "projected-key", true, nil
}

// TestProjectionRetryPublishesNewKeyAndCannotOverwriteNewAttempt проверяет повтор сохранения после сбоя и
// защиту результата более новой попытки.
func TestProjectionRetryPublishesNewKeyAndCannotOverwriteNewAttempt(t *testing.T) {
	s, p, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, err := s.Start(ctx, newSession(owner, nb))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("print(42)"))
	r := D.ExecuteRequest{ExecutionID: "first", CellID: "cell", SourceHash: hex.EncodeToString(sum[:]), TimeoutSeconds: 300}
	if _, err = s.Admit(ctx, owner, v.ID, r); err != nil {
		t.Fatal(err)
	}
	l, err := s.Claim(ctx, "worker")
	if err != nil {
		t.Fatal(err)
	}
	ev := D.AgentEvent{Seq: 1, SessionID: v.ID, ExecutionID: "first", Kind: "kernel_message", MsgType: "stream", Content: json.RawMessage(`{"name":"stdout","text":"42"}`)}
	if err = s.CommitEvents(ctx, l, []D.AgentEvent{ev}); err != nil {
		t.Fatal(err)
	}
	w := &projectionWriter{fail: true}
	if err = s.Project(ctx, w); err == nil {
		t.Fatal("storage failure ignored")
	}
	var key string
	if err = p.QueryRow(ctx, "SELECT file_key FROM notebooks WHERE id=$1", nb).Scan(&key); err != nil || key != "key" {
		t.Fatal(key, err)
	}
	if _, err = p.Exec(ctx, "UPDATE runtime_projections SET next_attempt=now() WHERE session_id=$1", v.ID); err != nil {
		t.Fatal(err)
	}
	w.fail = false
	if err = s.Project(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "SELECT file_key FROM notebooks WHERE id=$1", nb).Scan(&key); err != nil || key != "projected-key" {
		t.Fatal(key, err)
	}
	r.ExecutionID = "newer"
	if _, err = s.Admit(ctx, owner, v.ID, r); err != nil {
		t.Fatal(err)
	}
	l, err = s.Claim(ctx, "worker2")
	if err != nil {
		t.Fatal(err)
	}
	ev.Seq = 2
	if err = s.CommitEvents(ctx, l, []D.AgentEvent{ev}); err != nil {
		t.Fatal(err)
	}
	before := w.calls
	if err = s.Project(ctx, w); err != nil {
		t.Fatal(err)
	}
	if w.calls != before {
		t.Fatal("old attempt projected over newer attempt")
	}
	x, err := s.Execution(ctx, owner, v.ID, "first")
	if err != nil || x.Projection != "conflict" {
		t.Fatal(x, err)
	}
}

// selectiveProjectionWriter отклоняет выбранный результат, позволяя проверить обработку следующих заданий.
type selectiveProjectionWriter struct{}

// ApplyOutput имитирует запись результата в файл, включая управляемый отказ для проверки повтора.
func (selectiveProjectionWriter) ApplyOutput(_ context.Context, _ string, x D.Execution) (string, bool, error) {
	if x.ID == "a" {
		return "", false, errors.New("one output cannot be stored")
	}
	return "other-projection", true, nil
}

// TestFailedProjectionDoesNotBlockLaterJobs проверяет, что ошибка записи одного результата не останавливает
// следующие задания.
func TestFailedProjectionDoesNotBlockLaterJobs(t *testing.T) {
	s, _, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, err := s.Start(ctx, newSession(owner, nb))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("print(42)"))
	for _, id := range []string{"a", "z"} {
		if _, err = s.Admit(ctx, owner, v.ID, D.ExecuteRequest{ExecutionID: id, CellID: id, SourceHash: hex.EncodeToString(sum[:]), TimeoutSeconds: 300}); err != nil {
			t.Fatal(err)
		}
	}
	l, err := s.Claim(ctx, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CommitEvents(ctx, l, []D.AgentEvent{{Seq: 1, SessionID: v.ID, ExecutionID: "a", Kind: "kernel_message", MsgType: "stream", Content: json.RawMessage(`{"name":"stdout","text":"first"}`)}, {Seq: 2, SessionID: v.ID, ExecutionID: "z", Kind: "kernel_message", MsgType: "stream", Content: json.RawMessage(`{"name":"stdout","text":"second"}`)}}); err != nil {
		t.Fatal(err)
	}
	if err = s.Project(ctx, selectiveProjectionWriter{}); err == nil {
		t.Fatal("failed write was acknowledged")
	}
	if err = s.Project(ctx, selectiveProjectionWriter{}); err != nil {
		t.Fatal("one failed projection blocked later output", err)
	}
	x, err := s.Execution(ctx, owner, v.ID, "z")
	if err != nil || x.Projection != "applied" {
		t.Fatal(x, err)
	}
}
