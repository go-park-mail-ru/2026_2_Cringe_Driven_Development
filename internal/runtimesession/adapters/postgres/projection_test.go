package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"

	notebookusecase "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
)

// TestProjectionRetryPreservesRevisionAndCannotOverwriteNewAttempt проверяет повтор сохранения после сбоя и
// защиту результата более новой попытки.
func TestProjectionRetryPreservesRevisionAndCannotOverwriteNewAttempt(t *testing.T) {
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
	var before string
	if err = p.QueryRow(ctx, `SELECT revision FROM notebooks WHERE id=$1`, nb).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `CREATE FUNCTION reject_projection() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected storage failure'; END $$; CREATE TRIGGER reject_projection BEFORE UPDATE ON notebook_cell_data FOR EACH ROW EXECUTE FUNCTION reject_projection()`); err != nil {
		t.Fatal(err)
	}
	if err = s.Project(ctx); err == nil {
		t.Fatal("storage failure ignored")
	}
	var revision string
	if err = p.QueryRow(ctx, "SELECT revision FROM notebooks WHERE id=$1", nb).Scan(&revision); err != nil || revision != before {
		t.Fatal(revision, err)
	}
	if _, err = p.Exec(ctx, "UPDATE runtime_projections SET next_attempt=now() WHERE session_id=$1", v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `DROP TRIGGER reject_projection ON notebook_cell_data`); err != nil {
		t.Fatal(err)
	}
	if err = s.Project(ctx); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "SELECT revision FROM notebooks WHERE id=$1", nb).Scan(&revision); err != nil || revision != before {
		t.Fatal(revision, err)
	}
	notebook, err := s.notebooks.Get(ctx, nb, owner)
	if err != nil || string(notebook.Cells[0].Outputs) != `[{"name":"stdout","output_type":"stream","text":"42"}]` {
		t.Fatal("retry did not save result", err)
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
	before = revision
	beforeResult := string(notebook.Cells[0].Outputs)
	if err = s.Project(ctx); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, `SELECT revision FROM notebooks WHERE id=$1`, nb).Scan(&revision); err != nil || revision != before {
		t.Fatal("old attempt projected over newer attempt")
	}
	notebook, err = s.notebooks.Get(ctx, nb, owner)
	if err != nil || string(notebook.Cells[0].Outputs) != beforeResult {
		t.Fatal("old attempt changed result", err)
	}
	x, err := s.Execution(ctx, owner, v.ID, "first")
	if err != nil || x.Projection != "conflict" {
		t.Fatal(x, err)
	}
}

// TestFailedProjectionDoesNotBlockLaterJobs проверяет, что ошибка записи одного результата не останавливает
// следующие задания.
func TestFailedProjectionDoesNotBlockLaterJobs(t *testing.T) {
	s, pool, owner, nb := integrationStore(t)
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
	if _, err = pool.Exec(ctx, `CREATE FUNCTION reject_first() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.cell_id='a' THEN RAISE EXCEPTION 'injected storage failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_first BEFORE UPDATE ON notebook_cell_data FOR EACH ROW EXECUTE FUNCTION reject_first()`); err != nil {
		t.Fatal(err)
	}
	if err = s.Project(ctx); err == nil {
		t.Fatal("failed write was acknowledged")
	}
	if err = s.Project(ctx); err != nil {
		t.Fatal("one failed projection blocked later output", err)
	}
	x, err := s.Execution(ctx, owner, v.ID, "z")
	if err != nil || x.Projection != "applied" {
		t.Fatal(x, err)
	}
}

func TestProjectionRejectsSourceABA(t *testing.T) {
	s, _, owner, nb := integrationStore(t)
	ctx := context.Background()
	session, err := s.Start(ctx, newSession(owner, nb))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("print(42)"))
	_, err = s.Admit(ctx, owner, session.ID, D.ExecuteRequest{ExecutionID: "old", CellID: "cell", SourceHash: hex.EncodeToString(sum[:]), TimeoutSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	repo := s.notebooks
	n, err := repo.Get(ctx, nb, owner)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := repo.Apply(ctx, nb, owner, n.Revision, notebookusecase.Operation{Kind: notebookusecase.Edit, CellID: "cell", Source: "changed"})
	if err != nil {
		t.Fatal(err)
	}
	changed, err = repo.Apply(ctx, nb, owner, changed.Hash, notebookusecase.Operation{Kind: notebookusecase.Edit, CellID: "cell", Source: "print(42)"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.Claim(ctx, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CommitEvents(ctx, lease, []D.AgentEvent{{Seq: 1, SessionID: session.ID, ExecutionID: "old", Kind: "kernel_message", MsgType: "stream", Content: json.RawMessage(`{"text":"old result","name":"stdout"}`)}}); err != nil {
		t.Fatal(err)
	}
	if err = s.Project(ctx); err != nil {
		t.Fatal(err)
	}
	x, err := s.Execution(ctx, owner, session.ID, "old")
	if err != nil || x.Projection != "conflict" {
		t.Fatalf("ABA result published: %+v %v", x, err)
	}
	after, err := repo.Get(ctx, nb, owner)
	if err != nil || after.Revision != changed.Hash || string(after.Cells[0].Outputs) != "[]" {
		t.Fatal("stale result changed notebook")
	}
	// The projection failure must not finish or replace the actual runtime session.
	active, err := s.ActiveNotebook(ctx, owner)
	if err != nil || active != nb {
		t.Fatalf("session badge: %d %v", active, err)
	}
}
func TestProjectionAndAcknowledgementShareTransaction(t *testing.T) {
	s, pool, owner, nb := integrationStore(t)
	ctx := context.Background()
	session, err := s.Start(ctx, newSession(owner, nb))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("print(42)"))
	_, err = s.Admit(ctx, owner, session.ID, D.ExecuteRequest{ExecutionID: "run", CellID: "cell", SourceHash: hex.EncodeToString(sum[:]), TimeoutSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.Claim(ctx, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CommitEvents(ctx, lease, []D.AgentEvent{{Seq: 1, SessionID: session.ID, ExecutionID: "run", Kind: "kernel_message", MsgType: "stream", Content: json.RawMessage(`{"text":"42","name":"stdout"}`)}}); err != nil {
		t.Fatal(err)
	}
	before, err := s.notebooks.Get(ctx, nb, owner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_ack() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.data->'Public'->>'projection'='applied' THEN RAISE EXCEPTION 'injected projection acknowledgement failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_ack BEFORE UPDATE ON runtime_executions FOR EACH ROW EXECUTE FUNCTION fail_ack()`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Project(ctx); err == nil {
		t.Fatal("expected acknowledgement failure")
	}
	after, err := s.notebooks.Get(ctx, nb, owner)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("notebook committed without projection acknowledgement")
	}
}
