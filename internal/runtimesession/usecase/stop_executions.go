package usecase

import (
	"context"
	"errors"
	"time"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
)

type stopStore interface {
	StopExecutions(context.Context, int64, uuid.UUID, bool, func(context.Context, string, int64, []string) ([]D.AgentEvent, error)) error
}
type executionsAgent interface {
	StopExecutions(context.Context, uuid.UUID, string, []string) error
}

// StopExecutions останавливает команды, сохраняя окружение.
func (s *Service) StopExecutions(ctx context.Context, owner int64, sid uuid.UUID) error {
	return s.stopExecutions(ctx, owner, sid, false)
}
func (s *Service) stopExecutions(ctx context.Context, owner int64, sid uuid.UUID, resume bool) error {
	store, ok := s.store.(stopStore)
	if !ok {
		return D.ErrUnavailable
	}
	agent, ok := s.agent.(executionsAgent)
	if !ok {
		return D.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := store.StopExecutions(ctx, owner, sid, resume, func(ctx context.Context, operation string, after int64, ids []string) ([]D.AgentEvent, error) {
		stopErr := agent.StopExecutions(ctx, sid, operation, ids)
		if stopErr != nil {
			stopErr = D.ErrUnavailable
		}
		var all []D.AgentEvent
		for {
			events, err := s.agent.Events(ctx, sid, after, D.EventLimits{Count: 100, Bytes: 16 << 20})
			if err != nil {
				return all, D.ErrUnavailable
			}
			if len(events) == 0 {
				return all, stopErr
			}
			for _, ev := range events {
				if ev.SessionID != sid || ev.Seq <= after {
					return nil, D.ErrProtocol
				}
				after = ev.Seq
				all = append(all, ev)
			}
		}
	})
	if err != nil && !errors.Is(err, D.ErrNotFound) && !errors.Is(err, D.ErrConflict) {
		return D.ErrUnavailable
	}
	return err
}
