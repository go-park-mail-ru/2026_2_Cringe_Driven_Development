package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
)

// TestBatchConcurrentRetryAndStableQueue проверяет один пакет при конкурентном повторе и устойчивый порядок
// ячеек в очереди.
func TestBatchConcurrentRetryAndStableQueue(t *testing.T) {
	s, pool, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, e := s.Start(ctx, newSession(owner, nb))
	if e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256([]byte("print(42)"))
	r := D.BatchRequest{RequestID: "batch-1", TimeoutSeconds: 300, Cells: []D.BatchCell{{CellID: "a", SourceHash: hex.EncodeToString(hash[:])}, {CellID: "c", SourceHash: hex.EncodeToString(hash[:])}}}
	var wg sync.WaitGroup
	ids := make(chan D.Batch, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { b, err := s.AdmitBatch(ctx, owner, v.ID, r); ids <- b; errs <- err })
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	first := <-ids
	second := <-ids
	if len(first.ExecutionIDs) != 2 || first.ExecutionIDs[0] != second.ExecutionIDs[0] {
		t.Fatal("duplicate batch")
	}
	q, err := s.Queue(ctx, owner, v.ID)
	if err != nil || len(q.Items) != 2 || q.Items[0].CellID != "a" || q.Items[1].CellID != "c" {
		t.Fatal(q, err)
	}
	var count int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM runtime_executions").Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	r.Cells[0].SourceHash = string(make([]byte, 64))
	if _, err = s.AdmitBatch(ctx, owner, v.ID, r); !errors.Is(err, D.ErrConflict) {
		t.Fatal("changed retry accepted", err)
	}
}

// TestBatchFailureRollsBackEveryExecution проверяет откат всех элементов пакета при конфликте одной ячейки и
// изоляцию владельца.
func TestBatchFailureRollsBackEveryExecution(t *testing.T) {
	s, pool, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, err := s.Start(ctx, newSession(owner, nb))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("print(42)"))
	r := D.BatchRequest{RequestID: "batch", TimeoutSeconds: 300, Cells: []D.BatchCell{{CellID: "a", SourceHash: hex.EncodeToString(hash[:])}, {CellID: "b", SourceHash: strings.Repeat("a", 64)}}}
	if _, err = s.AdmitBatch(ctx, owner, v.ID, r); !errors.Is(err, D.ErrConflict) {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM runtime_executions").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial batch admitted", count, err)
	}
	if _, err = s.Queue(ctx, owner+100, v.ID); !errors.Is(err, D.ErrNotFound) {
		t.Fatal("owner isolation", err)
	}
}

// TestInspectionsAreDurableButNeverAppearInCellQueue проверяет сохранность инспекции, исключение из очереди
// ячеек и запрет restart до её завершения.
func TestInspectionsAreDurableButNeverAppearInCellQueue(t *testing.T) {
	s, _, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, err := s.Start(ctx, newSession(owner, nb))
	if err != nil {
		t.Fatal(err)
	}
	x, err := s.AdmitInspection(ctx, owner, v.ID, "inspect-1")
	if err != nil || x.Kind != "inspect" {
		t.Fatal(x, err)
	}
	if _, err = s.AdmitInspection(ctx, owner, v.ID, "inspect-1"); err != nil {
		t.Fatal(err)
	}
	q, err := s.Queue(ctx, owner, v.ID)
	if err != nil || len(q.Items) != 0 {
		t.Fatal(q, err)
	}
	called := false
	if _, err = s.Control(ctx, owner, v.ID, true, func(context.Context) (string, error) { called = true; return "", nil }); !errors.Is(err, D.ErrConflict) || called {
		t.Fatal("restart ignored pending inspection", err, called)
	}
	l, err := s.Claim(ctx, "inspection-test")
	if err != nil {
		t.Fatal(err)
	}
	err = s.CommitEvents(ctx, l, []D.AgentEvent{{SessionID: v.ID, Seq: 1, ExecutionID: x.ID, Kind: "execution_state", State: "SUCCEEDED", Result: json.RawMessage(`{"variables":[]}`)}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.Events(ctx, owner, v.ID, 0, D.EventLimits{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err = json.Unmarshal(page.Events[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["inspection_id"] != "inspect-1" || body["execution_id"] != nil {
		t.Fatal("inspection became a cell event", body)
	}
	x, err = s.Execution(ctx, owner, v.ID, x.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := x.Inspection()
	if err != nil || result.State != "SUCCEEDED" || result.Variables == nil {
		t.Fatal(result, err)
	}
}

// TestTerminalAdmissionReplySchedulesOutputWithoutEvents проверяет постановку результата на сохранение даже
// для завершённой команды без событий вывода.
func TestTerminalAdmissionReplySchedulesOutputWithoutEvents(t *testing.T) {
	s, pool, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, err := s.Start(ctx, newSession(owner, nb))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("print(42)"))
	x, err := s.Admit(ctx, owner, v.ID, D.ExecuteRequest{ExecutionID: "silent", CellID: "a", SourceHash: hex.EncodeToString(hash[:]), TimeoutSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.Claim(ctx, "terminal-reply")
	if err != nil {
		t.Fatal(err)
	}
	x.State = "SUCCEEDED"
	x.Dispatch = "ACCEPTED"
	if err = s.SaveExecution(ctx, lease, x); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM runtime_projections WHERE session_id=$1 AND execution_id=$2", v.ID, x.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("silent execution projection lost", count, err)
	}
}
