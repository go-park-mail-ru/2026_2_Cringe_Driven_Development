package usecase_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"github.com/google/uuid"
)

// readyPool создаёт в fake-облаке VPS пула с ждущим агентом и записывает её в пул как READY.
func (h *harness) readyPool(imageID string, withAgent bool) domain.PoolVPS {
	h.t.Helper()
	ctx := context.Background()
	vps := domain.PoolVPS{ID: uuid.New(), State: domain.PoolReady, ImageID: imageID, AgentVersion: h.s.AgentVersion,
		HostPrivateKey: []byte(sealPrefix + "pool-key"), HostPublicKey: "ssh-ed25519 AAAApool"}
	var err error
	if vps.Resources.PortID, vps.Resources.PrivateIP, err = h.cloud.EnsurePort(ctx, vps.Env()); err != nil {
		h.t.Fatal(err)
	}
	if vps.Resources.VolumeID, err = h.cloud.EnsureVolume(ctx, vps.Env()); err != nil {
		h.t.Fatal(err)
	}
	if vps.Resources.ServerID, err = h.cloud.Cloud.EnsureServer(ctx, vps.Env(), ""); err != nil {
		h.t.Fatal(err)
	}
	if withAgent {
		h.agent.RegisterWaiting("https://" + vps.Resources.PrivateIP + ":8443")
	}
	h.store.addPool(vps)
	return vps
}

func (h *harness) poolCount(state domain.PoolState) int {
	n := 0
	for _, s := range h.store.poolStates() {
		if s == state {
			n++
		}
	}
	return n
}

func TestPoolClaimSkipsCloudAndAnsible(t *testing.T) {
	h := newHarness(t)
	h.s.PoolSize = 2
	vps := h.readyPool("img-1", true)
	ensures := h.cloud.Calls("EnsureServer")
	sid := h.ready()
	env := h.env(sid)
	if got := fmt.Sprint(h.store.history(sid)); got != "[QUEUED VERIFYING READY]" {
		t.Fatalf("history = %s", got)
	}
	if env.PoolID != vps.ID || env.Resources != vps.Resources || env.Endpoint != "https://"+vps.Resources.PrivateIP+":8443" {
		t.Fatalf("env = %+v", env)
	}
	if h.host.Calls() != 0 || h.cloud.Calls("EnsureServer") != ensures {
		t.Fatalf("claim touched ansible (%d) or cloud", h.host.Calls())
	}
	if got := h.store.poolStates(); fmt.Sprint(got) != "[CLAIMED]" {
		t.Fatalf("pool = %v", got)
	}
	sec, _ := h.store.LoadSecrets(context.Background(), sid)
	if sec.HostPublicKey != "ssh-ed25519 AAAApool" || string(sec.Token) == "" {
		t.Fatalf("secrets = %+v", sec)
	}
}

func TestPoolEmptyCreatesNewVPS(t *testing.T) {
	h := newHarness(t)
	h.s.PoolSize = 2
	sid := h.ready()
	if got := fmt.Sprint(h.store.history(sid)); got != "[QUEUED CREATING CONFIGURING VERIFYING READY]" {
		t.Fatalf("history = %s", got)
	}
	if h.env(sid).PoolID != uuid.Nil || h.host.Calls() != 1 {
		t.Fatalf("env = %+v, ansible calls = %d", h.env(sid), h.host.Calls())
	}
}

func TestPoolLastVPSGoesToOneSession(t *testing.T) {
	h := newHarness(t)
	h.s.PoolSize = 1
	vps := h.readyPool("img-1", true)
	a, b := h.request(), h.request()
	var wg sync.WaitGroup
	for _, owner := range []string{"w1", "w2"} {
		w := h.worker(owner)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := runOnce(t, w); !ok || err != nil {
				t.Errorf("RunOnce = %v, %v", ok, err)
			}
		}()
	}
	wg.Wait()
	ea, eb := h.env(a), h.env(b)
	if ea.State != domain.StateReady || eb.State != domain.StateReady {
		t.Fatalf("states = %s, %s", ea.State, eb.State)
	}
	if (ea.PoolID == vps.ID) == (eb.PoolID == vps.ID) {
		t.Fatalf("pool ids = %s, %s; want exactly one %s", ea.PoolID, eb.PoolID, vps.ID)
	}
	if h.host.Calls() != 1 {
		t.Fatalf("ansible calls = %d, want 1 (the session without the pool vps)", h.host.Calls())
	}
}

func TestPoolAssignFailureCreatesNewVPS(t *testing.T) {
	h := newHarness(t)
	h.s.PoolSize = 1
	vps := h.readyPool("img-1", false) // агента нет: /assign не проходит
	sid := h.ready()
	env := h.env(sid)
	if got := fmt.Sprint(h.store.history(sid)); got != "[QUEUED VERIFYING CREATING CONFIGURING VERIFYING READY]" {
		t.Fatalf("history = %s", got)
	}
	if env.PoolID != uuid.Nil || env.Resources.ServerID == vps.Resources.ServerID {
		t.Fatalf("env = %+v", env)
	}
	if n := len(h.store.poolStates()); n != 0 {
		t.Fatalf("pool rows left = %d", n)
	}
	owned, _ := h.cloud.ListOwned(context.Background())
	for _, r := range owned {
		if r.PoolID == vps.ID {
			t.Fatalf("pool resource left: %+v", r)
		}
	}
}

func TestPoolReplenishedAfterClaim(t *testing.T) {
	h := newHarness(t)
	h.s.PoolSize = 2
	w := h.worker("w1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	waitFor(t, "pool of 2", func() bool { return h.poolCount(domain.PoolReady) == 2 })
	if h.host.Calls() != 2 {
		t.Fatalf("ansible calls = %d", h.host.Calls())
	}
	sid := h.request()
	waitFor(t, "session ready", func() bool { return h.env(sid).State == domain.StateReady })
	if h.env(sid).PoolID == uuid.Nil {
		t.Fatal("session did not get a pool vps")
	}
	waitFor(t, "pool refilled", func() bool { return h.poolCount(domain.PoolReady) == 2 })
	if h.poolCount(domain.PoolClaimed) != 1 {
		t.Fatalf("pool = %v", h.store.poolStates())
	}
}

func TestPoolPreparationFailureDeletesVPS(t *testing.T) {
	h := newHarness(t)
	h.s.PoolSize = 1
	h.host.Fail("Configure", errors.New("ansible failed"))
	w := h.worker("w1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	waitFor(t, "failed vps removed", func() bool { return h.host.Calls() == 1 && len(h.store.poolStates()) == 0 })
	cancel()
	<-done
	if n := h.resources(); n != 0 {
		t.Fatalf("resources left = %d", n)
	}
}

func TestPoolReconcileDeletesStaleAndOrphans(t *testing.T) {
	h := newHarness(t)
	h.s.PoolSize = 1
	stale := h.readyPool("img-old", true)
	current := h.readyPool("img-1", true)
	orphan := h.cloud.AddPool("volume", uuid.New())
	if err := usecase.Reconcile(context.Background(), h.worker("w1")); err != nil {
		t.Fatal(err)
	}
	if got := h.store.poolStates(); fmt.Sprint(got) != "[READY]" {
		t.Fatalf("pool = %v", got)
	}
	owned, _ := h.cloud.ListOwned(context.Background())
	for _, r := range owned {
		if r.PoolID == stale.ID || r.ID == orphan {
			t.Fatalf("resource left: %+v", r)
		}
	}
	if h.cloud.Count("server") != 1 || current.ID == uuid.Nil {
		t.Fatalf("servers = %d", h.cloud.Count("server"))
	}
}

// RUNTIME_WARM_POOL_SIZE=0: пул выключен целиком — нет пополнения и захвата, оставшиеся VPS удаляются.
func TestPoolSizeZeroDisablesPool(t *testing.T) {
	h := newHarness(t)
	h.s.PoolSize = 0
	h.readyPool("img-1", true)
	sid := h.ready()
	if got := fmt.Sprint(h.store.history(sid)); got != "[QUEUED CREATING CONFIGURING VERIFYING READY]" {
		t.Fatalf("history = %s", got)
	}
	w := h.worker("w1")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if h.store.reserves != 0 {
		t.Fatalf("pool reserved %d times", h.store.reserves)
	}
	if n := len(h.store.poolStates()); n != 0 {
		t.Fatalf("leftover pool rows = %d", n)
	}
	if h.cloud.Count("server") != 1 {
		t.Fatalf("servers = %d, want only the session vps", h.cloud.Count("server"))
	}
}
