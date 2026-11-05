package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/config"
	runtime "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime"
	env "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/adapters/environment"
	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	UC "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/usecase"
	"github.com/google/uuid"
)

// TestRuntimeDisabledHistoryAndStopIntent проверяет доступность истории и сохранение stop при отключённом
// создании окружений.
func TestRuntimeDisabledHistoryAndStopIntent(t *testing.T) {
	s, p, owner, nb := integrationStore(t)
	ctx := context.Background()
	v := newSession(owner, nb)
	v.AgentStarted = true
	v.CreateSent = true
	v, err := s.Start(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "INSERT INTO runtime_environments(session_id,operation_id,language,profile,state,desired_state,agent_version) VALUES($1,$2,'python','default','READY','READY','test')", v.ID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	module, err := runtime.Build(ctx, config.RuntimeConfig{}, p)
	if err != nil || module.Worker != nil {
		t.Fatal(module, err)
	}
	uc := UC.NewService(s, UC.Settings{}, nil)
	if _, err = uc.Start(ctx, owner, nb, D.StartRequest{RequestID: "disabled-start", Language: "r"}); !errors.Is(err, D.ErrUnavailable) {
		t.Fatal(err)
	}
	if history, e := uc.GetSession(ctx, owner, v.ID); e != nil || history.State != D.Ready {
		t.Fatal(history, e)
	}
	if err = uc.Stop(ctx, owner, v.ID); err != nil {
		t.Fatal(err)
	}
	worker := UC.NewWorker(s, env.New(module.Environments), module.Agent, nil)
	for range 7 {
		if _, err = p.Exec(ctx, "UPDATE runtime_sessions SET next_check=now() WHERE id=$1", v.ID); err != nil {
			t.Fatal(err)
		}
		l, e := s.Claim(ctx, "disabled-worker")
		if e != nil {
			t.Fatal(e)
		}
		if e = worker.Step(ctx, l); e != nil {
			t.Fatal(e)
		}
	}
	current, err := uc.GetSession(ctx, owner, v.ID)
	if err != nil || current.State != D.Stopped || !current.CleanupError || !current.JournalIncomplete || !current.DeleteSent {
		t.Fatal(current, err)
	}
	active, err := uc.GetActive(ctx, owner)
	if err != nil || active != nil {
		t.Fatal("stopped runtime still occupies user slot", active, err)
	}
	var desired string
	if err = p.QueryRow(ctx, "SELECT desired_state FROM runtime_environments WHERE session_id=$1", v.ID).Scan(&desired); err != nil || desired != "DELETED" {
		t.Fatal(desired, err)
	}
}
