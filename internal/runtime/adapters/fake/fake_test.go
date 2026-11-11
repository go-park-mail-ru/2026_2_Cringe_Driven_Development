package fake

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"github.com/google/uuid"
)

var (
	_ usecase.CloudProvider    = (*Cloud)(nil)
	_ usecase.HostConfigurator = (*Host)(nil)
	_ usecase.AgentClient      = (*Agent)(nil)
)

func newEnv() domain.Environment {
	return domain.Environment{SessionID: uuid.New(), OperationID: uuid.New(), Language: "python"}
}

func TestCloudEnsureIsIdempotentBySession(t *testing.T) {
	ctx := context.Background()
	c := NewCloud(0)
	env := newEnv()

	portID, ip, err := c.EnsurePort(ctx, env)
	if err != nil || portID == "" || ip == "" {
		t.Fatalf("EnsurePort = %q, %q, %v", portID, ip, err)
	}
	volID, err := c.EnsureVolume(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	// ID тома потерян — повтор находит тот же том по session_id.
	again, err := c.EnsureVolume(ctx, env)
	if err != nil || again != volID {
		t.Fatalf("EnsureVolume again = %q, %v; want %q", again, err, volID)
	}
	env.Resources = domain.Resources{PortID: portID, VolumeID: volID, PrivateIP: ip}
	srvID, err := c.EnsureServer(ctx, env, "#cloud-config")
	if err != nil || srvID == "" {
		t.Fatalf("EnsureServer = %q, %v", srvID, err)
	}
	p2, ip2, _ := c.EnsurePort(ctx, env)
	if p2 != portID || ip2 != ip {
		t.Fatalf("EnsurePort again = %q %q", p2, ip2)
	}
	for _, kind := range []string{"server", "volume", "port"} {
		if c.Count(kind) != 1 {
			t.Fatalf("Count(%s) = %d, want 1", kind, c.Count(kind))
		}
	}
	if c.Calls("EnsureVolume") != 2 {
		t.Fatalf("Calls(EnsureVolume) = %d", c.Calls("EnsureVolume"))
	}
	owned, _ := c.ListOwned(ctx)
	if len(owned) != 3 || owned[0].SessionID != env.SessionID {
		t.Fatalf("ListOwned = %+v", owned)
	}

	if err := c.DeleteServer(ctx, srvID); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteServer(ctx, srvID); err != nil {
		t.Fatalf("second delete must be nil, got %v", err)
	}
	_ = c.DeleteVolume(ctx, volID)
	_ = c.DeletePort(ctx, portID)
	if owned, _ := c.ListOwned(ctx); len(owned) != 0 {
		t.Fatalf("ListOwned after delete = %+v", owned)
	}
}

func TestCloudFailNextConsumedOnce(t *testing.T) {
	ctx := context.Background()
	c := NewCloud(0)
	boom := errors.New("boom")
	c.Fail("EnsureVolume", boom)
	if _, err := c.EnsureVolume(ctx, newEnv()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if c.Count("volume") != 0 {
		t.Fatal("failed call must not create a volume")
	}
	if _, err := c.EnsureVolume(ctx, newEnv()); err != nil {
		t.Fatalf("second call err = %v", err)
	}
}

func TestCloudFailAfterCreatesResource(t *testing.T) {
	ctx := context.Background()
	c := NewCloud(0)
	c.FailAfter["EnsureVolume"] = usecase.ErrCloudTransient
	env := newEnv()
	if _, err := c.EnsureVolume(ctx, env); !errors.Is(err, usecase.ErrCloudTransient) {
		t.Fatalf("err = %v", err)
	}
	if c.Count("volume") != 1 {
		t.Fatal("FailAfter must create the volume before failing")
	}
	if _, err := c.EnsureVolume(ctx, env); err != nil || c.Count("volume") != 1 {
		t.Fatalf("retry err = %v, count = %d", err, c.Count("volume"))
	}
}

func TestCloudDelayRespectsContext(t *testing.T) {
	c := NewCloud(time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.EnsureVolume(ctx, newEnv()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestCloudConcurrentUse(t *testing.T) {
	c := NewCloud(0)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env := newEnv()
			_, _, _ = c.EnsurePort(context.Background(), env)
			_, _ = c.ListOwned(context.Background())
		}()
	}
	wg.Wait()
	if c.Count("port") != 20 {
		t.Fatalf("Count(port) = %d", c.Count("port"))
	}
}

func TestHostConfigureStartsWaitingAgent(t *testing.T) {
	ctx := context.Background()
	_, host, agent := New(0)
	sid, name := uuid.New(), uuid.New()
	ep := usecase.AgentEndpoint{BaseURL: "https://10.0.0.5:8443", Token: "tok"}

	if _, _, err := agent.Health(ctx, ep); err == nil {
		t.Fatal("unconfigured agent must be unreachable")
	}
	if err := host.Configure(ctx, usecase.HostConfig{Name: name, PrivateIP: "10.0.0.5"}); err != nil {
		t.Fatal(err)
	}
	h, status, err := agent.Health(ctx, usecase.AgentEndpoint{BaseURL: ep.BaseURL})
	if err != nil || status != 200 || !h.Ready || h.State != "WAITING" || h.SessionID != "" {
		t.Fatalf("waiting Health = %+v, %d, %v", h, status, err)
	}
	if _, rStatus, _ := agent.Assign(ctx, ep, sid, "r"); rStatus != 400 {
		t.Fatalf("assign r status = %d", rStatus)
	}
	h, status, err = agent.Assign(ctx, ep, sid, "python")
	if err != nil || status != 200 || !h.Ready || h.SessionID != sid.String() || h.Language != "python" {
		t.Fatalf("Assign = %+v, %d, %v", h, status, err)
	}
	if _, status, _ := agent.Assign(ctx, ep, sid, "python"); status != 200 {
		t.Fatalf("repeated assign status = %d", status)
	}
	if _, status, _ := agent.Assign(ctx, ep, uuid.New(), "python"); status != 409 {
		t.Fatalf("conflicting assign status = %d", status)
	}
	if _, status, _ := agent.Health(ctx, usecase.AgentEndpoint{BaseURL: ep.BaseURL, Token: "bad"}); status != 401 {
		t.Fatalf("wrong token status = %d", status)
	}
	if host.Calls() != 1 || host.Last().Name != name {
		t.Fatalf("host calls = %d", host.Calls())
	}

	if err := agent.Drain(ctx, ep); err != nil {
		t.Fatal(err)
	}
	if h, status, _ := agent.Health(ctx, ep); h.Ready || status != 503 {
		t.Fatalf("after drain = %+v, %d", h, status)
	}
	if err := agent.Stop(ctx, ep); err != nil {
		t.Fatal(err)
	}
	if h, _, _ := agent.Health(ctx, ep); h.State != "STOPPED" {
		t.Fatalf("after stop = %+v", h)
	}
	if agent.Calls("Drain") != 1 || agent.Calls("Stop") != 1 || agent.Calls("Assign") != 4 {
		t.Fatal("calls not counted")
	}
}

func TestHostBlockAndFailNext(t *testing.T) {
	host := NewHost()
	host.Block = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Configure(ctx, usecase.HostConfig{Name: uuid.New()}) }()
	select {
	case <-done:
		t.Fatal("Configure must block")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}

	boom := errors.New("ansible failed")
	host = NewHost()
	host.FailNext["Configure"] = boom
	if err := host.Configure(context.Background(), usecase.HostConfig{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if err := host.Configure(context.Background(), usecase.HostConfig{}); err != nil {
		t.Fatalf("second err = %v", err)
	}
}

func TestAgentRegisterOverridesHealth(t *testing.T) {
	agent := NewAgent()
	foreign := uuid.New()
	agent.Register("https://10.0.0.7:8443", "tok", foreign, "python")
	h, status, err := agent.Health(context.Background(), usecase.AgentEndpoint{BaseURL: "https://10.0.0.7:8443", Token: "tok"})
	if err != nil || status != 200 || h.SessionID != foreign.String() {
		t.Fatalf("Health = %+v %d %v", h, status, err)
	}
	agent.SetStatus("https://10.0.0.7:8443", 503)
	if h, status, _ := agent.Health(context.Background(), usecase.AgentEndpoint{BaseURL: "https://10.0.0.7:8443", Token: "tok"}); status != 503 || h.Ready {
		t.Fatalf("Health after SetStatus = %+v %d", h, status)
	}
	agent.FailNext["Health"] = usecase.ErrCloudTransient
	if _, _, err := agent.Health(context.Background(), usecase.AgentEndpoint{}); !errors.Is(err, usecase.ErrCloudTransient) {
		t.Fatalf("FailNext err = %v", err)
	}
}

func TestAgentExecutePrint(t *testing.T) {
	ctx := context.Background()
	agent := NewAgent()
	ep := usecase.AgentEndpoint{BaseURL: "https://10.0.0.8:8443", Token: "tok"}
	sid := uuid.New()
	agent.Register(ep.BaseURL, ep.Token, sid, "python")

	if err := agent.Execute(ctx, ep, "smoke-1", "print(42)"); err != nil {
		t.Fatal(err)
	}
	state, err := agent.Execution(ctx, ep, "smoke-1")
	if err != nil || state != "SUCCEEDED" {
		t.Fatalf("Execution = %q, %v", state, err)
	}
	if _, err = agent.Execution(ctx, ep, "missing"); err == nil {
		t.Fatal("unknown execution must fail")
	}
	events, err := agent.Events(ctx, ep, 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("Events = %v, %v", events, err)
	}
	var ev struct {
		Seq         int    `json:"seq"`
		SessionID   string `json:"session_id"`
		ExecutionID string `json:"execution_id"`
		MsgType     string `json:"msg_type"`
		Content     struct {
			Name, Text string
		} `json:"content"`
	}
	if err := json.Unmarshal(events[0], &ev); err != nil {
		t.Fatal(err)
	}
	if ev.MsgType != "stream" || ev.Content.Name != "stdout" || ev.Content.Text != "42\n" ||
		ev.ExecutionID != "smoke-1" || ev.SessionID != sid.String() || ev.Seq != 1 {
		t.Fatalf("event = %+v", ev)
	}
	if after, _ := agent.Events(ctx, ep, 1); len(after) != 0 {
		t.Fatalf("Events after 1 = %v", after)
	}
}
