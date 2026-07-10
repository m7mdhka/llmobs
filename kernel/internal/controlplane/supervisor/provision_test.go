package supervisor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/executors"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/metrics"
	"github.com/m7mdhka/llmobs/kernel/internal/plugindata"
	"github.com/m7mdhka/llmobs/kernel/internal/plugintest"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

type fakeProvisioner struct {
	mu    sync.Mutex
	fail  bool
	calls int
}

func (f *fakeProvisioner) Provision(context.Context, string, []plugindata.CollectionSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail {
		return errors.New("migration boom")
	}
	return nil
}

func supWithProvisioner(t *testing.T, fake *plugintest.FakeBackend, prov Provisioner, clk *clock) *Supervisor {
	t.Helper()
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	spec := PluginSpec{
		ID:                  "acme/w",
		GrantedCapabilities: []string{"store"},
		Backend:             executors.Backend{URL: fake.URL()},
		Collections: []plugindata.CollectionSpec{{
			Name:   "dashboards",
			Fields: []plugindata.FieldSpec{{Name: "title", Type: plugindata.FieldString, Indexed: true}},
		}},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := New(StaticProvider{Specs: []PluginSpec{spec}}, executors.NewExternalURL(2*time.Second),
		signer, metrics.New(), log,
		Config{BaseBackoff: time.Second, MaxBackoff: 4 * time.Second, MaxFaults: 3, TokenTTL: 10 * time.Minute}, clk.now)
	sup.SetProvisioner(prov)
	return sup
}

// TestProvisionFailureDegrades: a failed collection migration is a HEALTH SIGNAL —
// the plugin degrades and never reaches running until provisioning succeeds.
func TestProvisionFailureDegrades(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	fake := plugintest.NewFakeBackend(pluginproto.Info{ID: "acme/w", Version: "1.0.0", PluginAPIVersion: "v1alpha1", Capabilities: []string{"store"}})
	defer fake.Close()
	prov := &fakeProvisioner{fail: true}
	sup := supWithProvisioner(t, fake, prov, clk)

	sup.Reconcile(ctx)
	if st := state(sup, "acme/w"); st != StateDegraded {
		t.Fatalf("failed migration must degrade, got %s", st)
	}
	if prov.calls == 0 {
		t.Fatal("provisioner should have been called")
	}

	// Migration recovers -> running.
	prov.mu.Lock()
	prov.fail = false
	prov.mu.Unlock()
	clk.add(10 * time.Second)
	sup.Reconcile(ctx)
	if st := state(sup, "acme/w"); st != StateRunning {
		t.Fatalf("after migration succeeds, plugin should run, got %s", st)
	}
}

// TestProvisionRestartIdempotentNoFault: a kernel restart (fresh supervisor, new
// key, empty state) re-provisions idempotently and returns the plugin to running
// with ZERO faults — an interrupted migration recovered on re-handshake must not
// count toward the disable cap (ADR-0023).
func TestProvisionRestartIdempotentNoFault(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	fake := plugintest.NewFakeBackend(pluginproto.Info{ID: "acme/w", Version: "1.0.0", PluginAPIVersion: "v1alpha1", Capabilities: []string{"store"}})
	defer fake.Close()

	prov := &fakeProvisioner{}
	sup1 := supWithProvisioner(t, fake, prov, clk)
	sup1.Reconcile(ctx)
	if state(sup1, "acme/w") != StateRunning {
		t.Fatal("precondition: first supervisor should provision + run")
	}

	// Restart: new supervisor, empty state; the SAME idempotent provisioner re-runs.
	sup2 := supWithProvisioner(t, fake, prov, clk)
	sup2.Reconcile(ctx)
	if st := state(sup2, "acme/w"); st != StateRunning {
		t.Fatalf("post-restart re-provision should run, got %s", st)
	}
	for _, p := range sup2.Snapshot() {
		if p.ID == "acme/w" && (p.Faults != 0 || p.RestartAttempts != 0) {
			t.Fatalf("restart re-provision must not fault: faults=%d attempts=%d", p.Faults, p.RestartAttempts)
		}
	}
	if prov.calls < 2 {
		t.Fatalf("provisioner should have run on both boots (idempotent), calls=%d", prov.calls)
	}
}
