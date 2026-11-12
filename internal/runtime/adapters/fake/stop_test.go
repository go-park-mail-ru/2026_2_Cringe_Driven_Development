package fake

import (
	"context"
	"testing"

	U "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"github.com/google/uuid"
)

func TestStopExecutionsPreservesReady(t *testing.T) {
	a := NewAgent()
	id := uuid.New()
	ep := U.AgentEndpoint{BaseURL: "fake-stop", Token: "token"}
	a.Register(ep.BaseURL, ep.Token, id, "python")
	if err := a.StopExecutions(context.Background(), ep, "operation", nil); err != nil {
		t.Fatal(err)
	}
	h, status, err := a.Health(context.Background(), ep)
	if err != nil || status != 200 || !h.Ready {
		t.Fatalf("%+v %d %v", h, status, err)
	}
}
