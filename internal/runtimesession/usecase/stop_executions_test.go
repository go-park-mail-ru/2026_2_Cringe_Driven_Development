package usecase

import (
	"context"
	"errors"
	"testing"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
)

type stopTestStore struct {
	Store
	run func(context.Context, int64, uuid.UUID, bool, func(context.Context, string, int64, []string) ([]D.AgentEvent, error)) error
}

func (s stopTestStore) StopExecutions(ctx context.Context, owner int64, id uuid.UUID, resume bool, call func(context.Context, string, int64, []string) ([]D.AgentEvent, error)) error {
	return s.run(ctx, owner, id, resume, call)
}

type stopTestAgent struct {
	SessionAgent
	stopErr error
	pages   [][]D.AgentEvent
}

func (a *stopTestAgent) StopExecutions(context.Context, uuid.UUID, string, []string) error {
	return a.stopErr
}
func (a *stopTestAgent) Events(_ context.Context, _ uuid.UUID, after int64, _ D.EventLimits) ([]D.AgentEvent, error) {
	if len(a.pages) == 0 {
		return nil, nil
	}
	page := a.pages[0]
	a.pages = a.pages[1:]
	return page, nil
}

func TestStopExecutionsArchivesAllPagesAndFailure(t *testing.T) {
	for _, stopErr := range []error{nil, D.ErrUnavailable, D.ErrConflict} {
		t.Run(func() string {
			if stopErr == nil {
				return "confirmed"
			}
			return stopErr.Error()
		}(), func(t *testing.T) {
			sid := uuid.New()
			a := &stopTestAgent{stopErr: stopErr, pages: [][]D.AgentEvent{{{SessionID: sid, Seq: 8, Kind: "execution_state", State: "RUNNING"}}, {{SessionID: sid, Seq: 9, Kind: "execution_state", State: "INTERRUPTED"}}}}
			store := stopTestStore{run: func(ctx context.Context, owner int64, id uuid.UUID, resume bool, call func(context.Context, string, int64, []string) ([]D.AgentEvent, error)) error {
				events, err := call(ctx, "operation", 7, []string{"run"})
				if len(events) != 2 || events[1].State != "INTERRUPTED" {
					t.Fatal(events)
				}
				return err
			}}
			err := NewService(store, nil, a).StopExecutions(context.Background(), 1, sid)
			want := stopErr
			if stopErr != nil {
				want = D.ErrUnavailable
			}
			if !errors.Is(err, want) {
				t.Fatal(err, stopErr)
			}
		})
	}
}
