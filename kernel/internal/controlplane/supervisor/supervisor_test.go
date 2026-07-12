package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/executors"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/metrics"
	"github.com/m7mdhka/llmobs/kernel/internal/plugintest"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
	"io"
	"log/slog"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func newSup(t *testing.T, fake *plugintest.FakeBackend, budget time.Duration, clk *clock) (*Supervisor, *plugintoken.Signer) {
	t.Helper()
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	prov := StaticProvider{Specs: []PluginSpec{{
		ID:                  "acme/widget",
		GrantedCapabilities: []string{"ingest", "query"},
		Backend:             executors.Backend{URL: fake.URL()},
		WatermarkBudget:     budget,
	}}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := New(prov, executors.NewExternalURL(2*time.Second), signer, metrics.New(), log,
		Config{BaseBackoff: time.Second, MaxBackoff: 4 * time.Second, MaxFaults: 3, TokenTTL: 10 * time.Minute}, clk.now)
	return sup, signer
}

func state(s *Supervisor, id string) State {
	for _, p := range s.Snapshot() {
		if p.ID == id {
			return State(p.State)
		}
	}
	return ""
}

// TestLifecycle drives the full state machine over real HTTP:
// handshake -> running -> (down) -> degraded -> auto-disabled -> enable -> running
// -> operator disable -> token revoked.
func TestLifecycle(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	fake := plugintest.NewFakeBackend(pluginproto.Info{
		ID: "acme/widget", Version: "1.0.0", PluginAPIVersion: "v1alpha1",
		Capabilities: []string{"ingest", "query"},
	})
	defer fake.Close()
	sup, _ := newSup(t, fake, 0, clk)

	// 1) handshake -> running, token issued.
	sup.Reconcile(ctx)
	if st := state(sup, "acme/widget"); st != StateRunning {
		t.Fatalf("want running, got %s", st)
	}
	if sup.ServiceTokenFor("acme/widget") == "" {
		t.Fatal("running plugin should have a service token")
	}

	// 2) backend goes down -> degraded, backing off, then auto-disabled at the cap.
	fake.SetDown(true)
	for i := 0; i < 5; i++ {
		clk.add(10 * time.Second) // clear any backoff gate
		sup.Reconcile(ctx)
	}
	if st := state(sup, "acme/widget"); st != StateDisabled {
		t.Fatalf("want auto-disabled after fault cap, got %s", st)
	}
	if sup.ServiceTokenFor("acme/widget") != "" {
		t.Fatal("disabled plugin token must be revoked")
	}

	// 3) recover the backend + operator re-enable -> running again.
	fake.SetDown(false)
	sup.Enable("acme/widget")
	clk.add(10 * time.Second)
	sup.Reconcile(ctx)
	if st := state(sup, "acme/widget"); st != StateRunning {
		t.Fatalf("want running after enable, got %s", st)
	}

	// 4) operator disable -> disabled, token revoked, no auto-recovery.
	sup.Disable("acme/widget", "maintenance")
	sup.Reconcile(ctx)
	if st := state(sup, "acme/widget"); st != StateDisabled {
		t.Fatalf("want disabled, got %s", st)
	}
	if sup.ServiceTokenFor("acme/widget") != "" {
		t.Fatal("operator-disabled plugin token must be revoked")
	}
}

// TestStaleWatermarkDegrades: a ready plugin whose functional watermark is stale
// past budget is degraded, not running (process-alive != work-happening).
func TestStaleWatermarkDegrades(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	fake := plugintest.NewFakeBackend(pluginproto.Info{
		ID: "acme/widget", Version: "1.0.0", PluginAPIVersion: "v1alpha1", Capabilities: []string{"ingest"},
	})
	defer fake.Close()
	sup, _ := newSup(t, fake, 30*time.Second, clk)

	// Ready, but last progress was 60s ago vs a 30s budget.
	fake.SetHealth(pluginproto.Health{Live: true, Ready: true,
		Watermark: &pluginproto.Watermark{LastProgressUnix: clk.now().Add(-60 * time.Second).Unix()}})
	sup.Reconcile(ctx)
	if st := state(sup, "acme/widget"); st != StateDegraded {
		t.Fatalf("stale watermark should degrade, got %s", st)
	}

	// Progress catches up -> running.
	fake.SetHealth(pluginproto.Health{Live: true, Ready: true,
		Watermark: &pluginproto.Watermark{LastProgressUnix: clk.now().Unix()}})
	clk.add(10 * time.Second)
	sup.Reconcile(ctx)
	if st := state(sup, "acme/widget"); st != StateRunning {
		t.Fatalf("fresh watermark should run, got %s", st)
	}
}

// TestKernelRestartReHandshakeIsNotAFault is the ruled property: a
// kernel restart rotates the in-memory signing key and drops in-memory state, so
// the fresh supervisor must re-handshake every healthy plugin and return it to
// running WITHOUT counting a fault toward auto-disable.
func TestKernelRestartReHandshakeIsNotAFault(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	fake := plugintest.NewFakeBackend(pluginproto.Info{
		ID: "acme/widget", Version: "1.0.0", PluginAPIVersion: "v1alpha1", Capabilities: []string{"ingest"},
	})
	defer fake.Close()

	sup1, _ := newSup(t, fake, 0, clk)
	sup1.Reconcile(ctx)
	if state(sup1, "acme/widget") != StateRunning {
		t.Fatal("precondition: first supervisor should run the plugin")
	}

	// Simulate a kernel restart: a brand-new supervisor with a NEW signing key and
	// empty state (the old token no longer verifies and is gone).
	sup2, _ := newSup(t, fake, 0, clk)
	sup2.Reconcile(ctx)
	if st := state(sup2, "acme/widget"); st != StateRunning {
		t.Fatalf("post-restart re-handshake should run, got %s", st)
	}
	for _, p := range sup2.Snapshot() {
		if p.ID == "acme/widget" && (p.Faults != 0 || p.RestartAttempts != 0) {
			t.Fatalf("restart re-handshake must not count as a fault: faults=%d attempts=%d", p.Faults, p.RestartAttempts)
		}
	}
}

// TestHandshakeMismatchFaults: a backend whose info fails the manifest check
// (capability over-reach) faults rather than running.
func TestHandshakeMismatchFaults(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	// Backend claims a capability the manifest did not grant.
	fake := plugintest.NewFakeBackend(pluginproto.Info{
		ID: "acme/widget", Version: "1.0.0", PluginAPIVersion: "v1alpha1",
		Capabilities: []string{"ingest", "query", "secrets"},
	})
	defer fake.Close()
	sup, _ := newSup(t, fake, 0, clk) // manifest grants only ingest, query
	sup.Reconcile(ctx)
	if st := state(sup, "acme/widget"); st == StateRunning {
		t.Fatal("capability over-reach must not run")
	}
}

// TestOpsHandler: the read snapshot is public to authenticated callers; mutations
// are gated by isAllowed.
func TestOpsHandler(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	fake := plugintest.NewFakeBackend(pluginproto.Info{
		ID: "acme/widget", Version: "1.0.0", PluginAPIVersion: "v1alpha1", Capabilities: []string{"ingest"},
	})
	defer fake.Close()
	sup, _ := newSup(t, fake, 0, clk)
	sup.Reconcile(ctx)

	h := sup.Handler("/v1alpha1/supervisor", func(*http.Request) bool { return false })
	// read allowed
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1alpha1/supervisor/plugins", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot GET should be 200, got %d", rec.Code)
	}
	// mutation forbidden when isAllowed=false
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1alpha1/supervisor/plugins/acme/widget/disable", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mutation should be forbidden, got %d", rec.Code)
	}
}
