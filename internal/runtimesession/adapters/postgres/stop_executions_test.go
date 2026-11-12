package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
)

func TestStopExecutionsBlocksAdmissionAndCancelsPending(t *testing.T) {
	s, _, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, err := s.Start(ctx, newSession(owner, nb))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.AdmitInspection(ctx, owner, v.ID, "pending")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.StopExecutions(ctx, owner, v.ID, false, func(_ context.Context, _ string, _ int64, ids []string) ([]D.AgentEvent, error) {
			if len(ids) != 1 || ids[0] != "pending" {
				t.Errorf("bad stop snapshot: %v", ids)
			}
			close(entered)
			<-release
			return nil, nil
		})
	}()
	<-entered
	if _, err = s.AdmitInspection(ctx, owner, v.ID, "new"); !errors.Is(err, D.ErrConflict) {
		t.Fatal(err)
	}
	if err = s.StopExecutions(ctx, owner, v.ID, false, nil); !errors.Is(err, D.ErrConflict) {
		t.Fatal(err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	x, err := s.Execution(ctx, owner, v.ID, "pending")
	if err != nil || x.State != "CANCELLED" {
		t.Fatal(x, err)
	}
	after, err := s.Owned(ctx, owner, v.ID)
	if err != nil || after.State != D.Ready || after.Generation != v.Generation || after.StopOperation != "" {
		t.Fatal(after, err)
	}
	if _, err = s.AdmitInspection(ctx, owner, v.ID, "after"); err != nil {
		t.Fatal(err)
	}
}

func TestStopExecutionsWaitsForDispatchAndPreservesBarrier(t *testing.T) {
	s, _, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, err := s.Start(ctx, newSession(owner, nb))
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.Claim(ctx, "dispatch")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	dispatched := make(chan error, 1)
	go func() {
		dispatched <- s.DispatchGuard(ctx, l, func(context.Context) error { close(entered); <-release; return nil })
	}()
	<-entered
	stopped := make(chan error, 1)
	go func() {
		stopped <- s.StopExecutions(ctx, owner, v.ID, false, func(context.Context, string, int64, []string) ([]D.AgentEvent, error) { return nil, nil })
	}()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		current, e := s.Owned(waitCtx, owner, v.ID)
		if e != nil {
			t.Fatal(e)
		}
		if current.StopOperation != "" {
			break
		}
		select {
		case e := <-stopped:
			t.Fatal("stop did not wait for dispatch", e)
		case <-waitCtx.Done():
			t.Fatal(waitCtx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if _, err = s.AdmitInspection(ctx, owner, v.ID, "blocked"); !errors.Is(err, D.ErrConflict) {
		t.Fatal(err)
	}
	close(release)
	if err = <-dispatched; err != nil {
		t.Fatal(err)
	}
	if err = <-stopped; err != nil {
		t.Fatal(err)
	}
	if err = s.CheckLease(ctx, l); !errors.Is(err, D.ErrLeaseLost) {
		t.Fatal(err)
	}
}

func TestStopExecutionsResumesAfterLostResponse(t *testing.T) {
	s, _, owner, nb := integrationStore(t)
	ctx := context.Background()
	v, err := s.Start(ctx, newSession(owner, nb))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.AdmitInspection(ctx, owner, v.ID, "pending")
	if err != nil {
		t.Fatal(err)
	}
	var operation string
	err = s.StopExecutions(ctx, owner, v.ID, false, func(_ context.Context, id string, _ int64, _ []string) ([]D.AgentEvent, error) {
		operation = id
		return nil, D.ErrUnavailable
	})
	if !errors.Is(err, D.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err = s.AdmitInspection(ctx, owner, v.ID, "blocked"); !errors.Is(err, D.ErrConflict) {
		t.Fatal(err)
	}
	if err = s.StopExecutions(ctx, owner, v.ID, true, func(_ context.Context, id string, _ int64, _ []string) ([]D.AgentEvent, error) {
		if id != operation {
			t.Errorf("operation changed: %s != %s", id, operation)
		}
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	x, err := s.Execution(ctx, owner, v.ID, "pending")
	if err != nil || x.State != "CANCELLED" {
		t.Fatal(x, err)
	}
	page, err := s.Events(ctx, owner, v.ID, 0, D.EventLimits{Count: 100})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ev := range page.Events {
		if ev.Kind == "execution_state" {
			var body map[string]any
			if err = json.Unmarshal(ev.Body, &body); err != nil {
				t.Fatal(err)
			}
			if body["inspection_id"] == "pending" && body["state"] == "CANCELLED" {
				found = true
			}
			if _, bad := body["execution_id"]; bad {
				t.Fatal("inspection leaked as execution")
			}
		}
	}
	if !found {
		t.Fatal("cancel event missing")
	}
}

func TestStopExecutionsArchivesActiveOutputAndCancelsRunAll(t *testing.T) {
	s, _, owner, nb := integrationStore(t)
	ctx := context.Background()
	v := newSession(owner, nb)
	v.Generation = "same-generation"
	v, err := s.Start(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("print(42)"))
	hash := hex.EncodeToString(sum[:])
	batch, err := s.AdmitBatch(ctx, owner, v.ID, D.BatchRequest{RequestID: "batch", TimeoutSeconds: 300, Cells: []D.BatchCell{{CellID: "a", SourceHash: hash}, {CellID: "b", SourceHash: hash}, {CellID: "c", SourceHash: hash}}})
	if err != nil {
		t.Fatal(err)
	}
	active, err := s.Execution(ctx, owner, v.ID, batch.ExecutionIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.Claim(ctx, "active")
	if err != nil {
		t.Fatal(err)
	}
	active.Dispatch = "ACCEPTED"
	active.State = "RUNNING"
	if err = s.SaveExecution(ctx, l, active); err != nil {
		t.Fatal(err)
	}
	err = s.StopExecutions(ctx, owner, v.ID, false, func(context.Context, string, int64, []string) ([]D.AgentEvent, error) {
		return []D.AgentEvent{
			{SessionID: v.ID, ExecutionID: active.ID, Seq: 1, Kind: "kernel_message", MsgType: "stream", Content: json.RawMessage(`{"name":"stdout","text":"before interrupt"}`)},
			{SessionID: v.ID, ExecutionID: active.ID, Seq: 2, Kind: "execution_state", State: "INTERRUPTED", Result: json.RawMessage(`{"status":"error"}`)},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range batch.ExecutionIDs {
		x, e := s.Execution(ctx, owner, v.ID, id)
		if e != nil {
			t.Fatal(e)
		}
		want := "CANCELLED"
		if i == 0 {
			want = "INTERRUPTED"
			if len(x.Outputs) != 1 || !strings.Contains(string(x.Outputs[0]), "before interrupt") {
				t.Fatal(x.Outputs)
			}
		}
		if x.State != want {
			t.Fatal(x.State, want)
		}
	}
	queue, err := s.Queue(ctx, owner, v.ID)
	if err != nil || len(queue.Items) != 0 {
		t.Fatal(queue, err)
	}
	after, err := s.Owned(ctx, owner, v.ID)
	if err != nil || after.State != D.Ready || after.Generation != v.Generation {
		t.Fatal(after, err)
	}
	if err = s.StopExecutions(ctx, owner, v.ID, false, func(context.Context, string, int64, []string) ([]D.AgentEvent, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
}
