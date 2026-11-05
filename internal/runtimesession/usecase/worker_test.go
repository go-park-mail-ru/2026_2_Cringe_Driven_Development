package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/google/uuid"
	"go.uber.org/mock/gomock"
)

// workerMocks создаёт согласованные моки зависимостей для одного сценария фоновой обработки.
func workerMocks(t *testing.T) (*MockStore, *MockEnvironments, *MockSessionAgent, D.Lease) {
	t.Helper()
	c := gomock.NewController(t)
	s := NewMockStore(c)
	e := NewMockEnvironments(c)
	a := NewMockSessionAgent(c)
	l := D.Lease{Version: 1, Session: D.Session{ID: uuid.New(), Language: "python", State: D.Provisioning, Stage: "queued"}}
	s.EXPECT().CheckLease(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	return s, e, a, l
}

// TestCreateReplyLostReusesSessionID проверяет повтор запроса подготовки с тем же UUID после потери ответа.
func TestCreateReplyLostReusesSessionID(t *testing.T) {
	s, e, a, l := workerMocks(t)
	ctx := context.Background()
	e.EXPECT().Get(gomock.Any(), l.Session.ID).Return(D.Environment{}, D.ErrNotFound)
	s.EXPECT().Checkpoint(gomock.Any(), l, gomock.Any()).DoAndReturn(func(_ context.Context, _ D.Lease, v D.Session) error {
		if !v.CreateSent || !v.CreateUnknown {
			t.Fatal("missing durable intent")
		}
		return nil
	})
	e.EXPECT().RequestCreate(gomock.Any(), D.EnvironmentRequest{SessionID: l.Session.ID, Language: "python", Profile: "default"}).Return(D.Environment{}, errors.New("reply lost"))
	var saved D.Session
	s.EXPECT().Save(gomock.Any(), l, gomock.Any()).DoAndReturn(func(_ context.Context, _ D.Lease, v D.Session) error { saved = v; return nil })
	if err := NewWorker(s, e, a, nil, nil).Step(ctx, l); err != nil {
		t.Fatal(err)
	}
	if saved.ID != l.Session.ID || !saved.CreateUnknown || saved.State != D.Provisioning {
		t.Fatal(saved)
	}
}

// TestArchiveDBFailureDoesNotAuthorizeDeletion проверяет запрет удаления окружения после ошибки записи
// доступного журнала в БД.
func TestArchiveDBFailureDoesNotAuthorizeDeletion(t *testing.T) {
	s, e, a, l := workerMocks(t)
	l.Session.State = D.Stopping
	l.Session.AgentStarted = true
	l.Session.Reason = "requested"
	e.EXPECT().Get(gomock.Any(), l.Session.ID).Return(D.Environment{SessionID: l.Session.ID, State: "READY"}, nil)
	a.EXPECT().Drain(gomock.Any(), l.Session.ID).Return(nil)
	a.EXPECT().Stop(gomock.Any(), l.Session.ID).Return(nil)
	a.EXPECT().Events(gomock.Any(), l.Session.ID, int64(0), gomock.Any()).Return([]D.AgentEvent{{Seq: 1, SessionID: l.Session.ID, Kind: "kernel_message"}}, nil)
	fail := errors.New("db failed")
	s.EXPECT().CommitEvents(gomock.Any(), l, gomock.Any()).Return(fail)
	if err := NewWorker(s, e, a, nil, nil).Step(context.Background(), l); !errors.Is(err, fail) {
		t.Fatal(err)
	}
}

// TestArchiveCommitsBeforeRequestDelete проверяет сохранение доступных событий до запроса удаления
// окружения.
func TestArchiveCommitsBeforeRequestDelete(t *testing.T) {
	s, e, a, l := workerMocks(t)
	l.Session.State = D.Stopping
	l.Session.AgentStarted = true
	l.Session.Reason = "requested"
	e.EXPECT().Get(gomock.Any(), l.Session.ID).Return(D.Environment{SessionID: l.Session.ID, State: "READY"}, nil)
	gomock.InOrder(a.EXPECT().Drain(gomock.Any(), l.Session.ID).Return(nil), a.EXPECT().Stop(gomock.Any(), l.Session.ID).Return(nil), a.EXPECT().Events(gomock.Any(), l.Session.ID, int64(0), gomock.Any()).Return([]D.AgentEvent{}, nil), s.EXPECT().Executions(gomock.Any(), l.Session.ID).Return(nil, nil), s.EXPECT().Checkpoint(gomock.Any(), l, gomock.Any()).DoAndReturn(func(_ context.Context, _ D.Lease, v D.Session) error {
		if !v.JournalArchived {
			t.Fatal("delete without archive")
		}
		return nil
	}), e.EXPECT().RequestDelete(gomock.Any(), l.Session.ID, "requested").Return(nil))
	s.EXPECT().Save(gomock.Any(), l, gomock.Any()).DoAndReturn(func(_ context.Context, _ D.Lease, v D.Session) error {
		if v.State != D.Stopping {
			t.Fatal("cleanup falsely reported terminal")
		}
		return nil
	})
	if err := NewWorker(s, e, a, nil, func() time.Time { return time.Unix(100, 0) }).Step(context.Background(), l); err != nil {
		t.Fatal(err)
	}
}

// TestReconnectRecoversAfterCompleteFlow проверяет возврат сессии в рабочее состояние после восстановления
// связи.
func TestReconnectRecoversAfterCompleteFlow(t *testing.T) {
	s, e, a, l := workerMocks(t)
	l.Session.State = D.Reconnecting
	l.Session.Retry = 2
	e.EXPECT().Get(gomock.Any(), l.Session.ID).Return(D.Environment{SessionID: l.Session.ID, State: "READY"}, nil)
	a.EXPECT().Health(gomock.Any(), l.Session.ID).Return(D.Health{SessionID: l.Session.ID, Language: "python", State: "READY", Ready: true}, nil)
	a.EXPECT().Events(gomock.Any(), l.Session.ID, int64(0), gomock.Any()).Return([]D.AgentEvent{}, nil)
	s.EXPECT().Executions(gomock.Any(), l.Session.ID).Return(nil, nil)
	s.EXPECT().Save(gomock.Any(), l, gomock.Any()).DoAndReturn(func(_ context.Context, _ D.Lease, v D.Session) error {
		if v.State != D.Ready || v.Retry != 0 {
			t.Fatalf("successful reconnect not recovered: %+v", v)
		}
		return nil
	})
	if err := NewWorker(s, e, a, nil, nil).Step(context.Background(), l); err != nil {
		t.Fatal(err)
	}
}

// TestReconnectHealthCannotHideBrokenEvents проверяет, что успешный health не маскирует недоступный журнал
// событий.
func TestReconnectHealthCannotHideBrokenEvents(t *testing.T) {
	s, e, a, l := workerMocks(t)
	l.Session.State = D.Reconnecting
	l.Session.Retry = 5
	e.EXPECT().Get(gomock.Any(), l.Session.ID).Return(D.Environment{SessionID: l.Session.ID, State: "READY"}, nil)
	a.EXPECT().Health(gomock.Any(), l.Session.ID).Return(D.Health{SessionID: l.Session.ID, Language: "python", State: "READY", Ready: true}, nil)
	a.EXPECT().Events(gomock.Any(), l.Session.ID, int64(0), gomock.Any()).Return(nil, D.ErrUnavailable)
	s.EXPECT().Save(gomock.Any(), l, gomock.Any()).DoAndReturn(func(_ context.Context, _ D.Lease, v D.Session) error {
		if v.State != D.Stopping || v.Reason != "agent_unreachable" {
			t.Fatalf("healthy /health hid missing journal: %+v", v)
		}
		return nil
	})
	if err := NewWorker(s, e, a, nil, nil).Step(context.Background(), l); err != nil {
		t.Fatal(err)
	}
}

// TestSixJournalFailuresAllowIncompleteDelete проверяет ограниченный срок ожидания недоступного журнала и
// явную отметку неполноты перед очисткой.
func TestSixJournalFailuresAllowIncompleteDelete(t *testing.T) {
	s, e, a, l := workerMocks(t)
	l.Session.State = D.Stopping
	l.Session.AgentStarted = true
	l.Session.Reason = "agent_unreachable"
	l.Session.JournalRetry = 5
	e.EXPECT().Get(gomock.Any(), l.Session.ID).Return(D.Environment{SessionID: l.Session.ID, State: "READY"}, nil)
	a.EXPECT().Drain(gomock.Any(), l.Session.ID).Return(D.ErrUnavailable)
	a.EXPECT().Stop(gomock.Any(), l.Session.ID).Return(D.ErrUnavailable)
	a.EXPECT().Events(gomock.Any(), l.Session.ID, int64(0), gomock.Any()).Return(nil, D.ErrUnavailable)
	s.EXPECT().Executions(gomock.Any(), l.Session.ID).Return(nil, nil)
	s.EXPECT().Checkpoint(gomock.Any(), l, gomock.Any()).DoAndReturn(func(_ context.Context, _ D.Lease, v D.Session) error {
		if !v.JournalArchived || !v.JournalIncomplete {
			t.Fatal(v)
		}
		return nil
	})
	e.EXPECT().RequestDelete(gomock.Any(), l.Session.ID, "agent_unreachable").Return(nil)
	s.EXPECT().Save(gomock.Any(), l, gomock.Any()).Return(nil)
	if err := NewWorker(s, e, a, nil, nil).Step(context.Background(), l); err != nil {
		t.Fatal(err)
	}
}

// TestAgentLostAfterProvisioningDoesNotHoldStartupForever проверяет ограничение ожидания агента после
// подготовки окружения.
func TestAgentLostAfterProvisioningDoesNotHoldStartupForever(t *testing.T) {
	s, e, a, l := workerMocks(t)
	e.EXPECT().Get(gomock.Any(), l.Session.ID).Return(D.Environment{SessionID: l.Session.ID, State: "READY"}, nil)
	a.EXPECT().Health(gomock.Any(), l.Session.ID).Return(D.Health{}, D.ErrUnavailable)
	s.EXPECT().Save(gomock.Any(), l, gomock.Any()).DoAndReturn(func(_ context.Context, _ D.Lease, v D.Session) error {
		if v.State != D.Reconnecting || v.Retry != 1 || !v.AgentStarted {
			t.Fatalf("lost agent left startup unbounded: %+v", v)
		}
		return nil
	})
	if err := NewWorker(s, e, a, nil, nil).Step(context.Background(), l); err != nil {
		t.Fatal(err)
	}
}

// TestProtocolFailureStartsCleanupInsteadOfRetryingDBForever проверяет переход к очистке при нарушении
// протокола вместо бесконечного повтора.
func TestProtocolFailureStartsCleanupInsteadOfRetryingDBForever(t *testing.T) {
	s, e, a, l := workerMocks(t)
	l.Session.State = D.Ready
	l.Session.AgentStarted = true
	e.EXPECT().Get(gomock.Any(), l.Session.ID).Return(D.Environment{SessionID: l.Session.ID, State: "READY"}, nil)
	a.EXPECT().Health(gomock.Any(), l.Session.ID).Return(D.Health{SessionID: l.Session.ID, Language: "python", State: "READY", Ready: true}, nil)
	a.EXPECT().Events(gomock.Any(), l.Session.ID, int64(0), gomock.Any()).Return([]D.AgentEvent{{Seq: 1, SessionID: l.Session.ID}}, nil)
	s.EXPECT().CommitEvents(gomock.Any(), l, gomock.Any()).Return(D.ErrProtocol)
	s.EXPECT().Save(gomock.Any(), l, gomock.Any()).DoAndReturn(func(_ context.Context, _ D.Lease, v D.Session) error {
		if v.State != D.Stopping || v.Reason != "protocol_error" {
			t.Fatal(v)
		}
		return nil
	})
	if err := NewWorker(s, e, a, nil, nil).Step(context.Background(), l); err != nil {
		t.Fatal(err)
	}
}
