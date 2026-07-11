package supervisor

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/executors"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/metrics"
	"github.com/m7mdhka/llmobs/kernel/internal/plugintest"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// TestTokenDeliveredAndScoped: reaching running means the kernel PUSHED the
// service token to the plugin's own URL, and the delivered token verifies + is
// scoped to that plugin (H7c).
func TestTokenDeliveredAndScoped(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	fake := plugintest.NewFakeBackend(pluginproto.Info{ID: "acme/widget", Version: "1.0.0", PluginAPIVersion: "v1alpha1", Capabilities: []string{"ingest", "query"}})
	defer fake.Close()
	sup, signer := newSup(t, fake, 0, clk)

	sup.Reconcile(ctx)
	if state(sup, "acme/widget") != StateRunning {
		t.Fatal("plugin should reach running after token delivery")
	}
	tok := fake.DeliveredToken()
	if tok == "" {
		t.Fatal("service token must be delivered to the plugin (H7c)")
	}
	claims, err := signer.VerifyServiceToken(context.Background(), tok, clk.now().Add(time.Minute))
	if err != nil {
		t.Fatalf("delivered token must verify: %v", err)
	}
	if claims.PluginID != "acme/widget" {
		t.Fatalf("delivered token must be scoped to the plugin, got %s", claims.PluginID)
	}
}

// TestTokenDeliveryFailureDegrades: token delivery is part of READINESS — a plugin
// that cannot receive its token degrades and never reaches running (it does not
// fail open).
func TestTokenDeliveryFailureDegrades(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	fake := plugintest.NewFakeBackend(pluginproto.Info{ID: "acme/widget", Version: "1.0.0", PluginAPIVersion: "v1alpha1", Capabilities: []string{"ingest"}})
	defer fake.Close()
	fake.SetTokenFail(true)
	sup, _ := newSup(t, fake, 0, clk)

	sup.Reconcile(ctx)
	if st := state(sup, "acme/widget"); st != StateDegraded {
		t.Fatalf("token delivery failure must degrade (no token => not ready), got %s", st)
	}
	if fake.DeliveredToken() != "" {
		t.Fatal("no token should be recorded when delivery fails")
	}

	// Recovery: delivery succeeds -> running.
	fake.SetTokenFail(false)
	clk.add(10 * time.Second)
	sup.Reconcile(ctx)
	if st := state(sup, "acme/widget"); st != StateRunning {
		t.Fatalf("after delivery recovers, plugin should run, got %s", st)
	}
}

// TestTokenDeliveryPerPluginNoCrossDelivery is H7c's prove-the-negative: delivery
// is kernel-initiated to EACH plugin's own registered URL, so each backend receives
// only ITS OWN token, scoped to its own id. There is no plugin-pull path, so
// "obtain another plugin's token" is not an expressible operation — each backend
// can only ever hold the token the kernel pushed to it.
func TestTokenDeliveryPerPluginNoCrossDelivery(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	fa := plugintest.NewFakeBackend(pluginproto.Info{ID: "acme/a", Version: "1.0.0", PluginAPIVersion: "v1alpha1", Capabilities: []string{"ingest"}})
	defer fa.Close()
	fb := plugintest.NewFakeBackend(pluginproto.Info{ID: "acme/b", Version: "1.0.0", PluginAPIVersion: "v1alpha1", Capabilities: []string{"ingest"}})
	defer fb.Close()
	prov := StaticProvider{Specs: []PluginSpec{
		{ID: "acme/a", GrantedCapabilities: []string{"ingest"}, Backend: executors.Backend{URL: fa.URL()}},
		{ID: "acme/b", GrantedCapabilities: []string{"ingest"}, Backend: executors.Backend{URL: fb.URL()}},
	}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := New(prov, executors.NewExternalURL(2*time.Second), signer, metrics.New(), log,
		Config{BaseBackoff: time.Second, MaxBackoff: 4 * time.Second, MaxFaults: 3, TokenTTL: 10 * time.Minute}, clk.now)

	sup.Reconcile(ctx)

	ta, tb := fa.DeliveredToken(), fb.DeliveredToken()
	if ta == "" || tb == "" {
		t.Fatal("both plugins should receive their own token")
	}
	if ta == tb {
		t.Fatal("each plugin must receive a distinct token")
	}
	ca, err := signer.VerifyServiceToken(context.Background(), ta, clk.now().Add(time.Minute))
	if err != nil || ca.PluginID != "acme/a" {
		t.Fatalf("plugin A's backend must hold A's token, got %s (%v)", ca.PluginID, err)
	}
	cb, err := signer.VerifyServiceToken(context.Background(), tb, clk.now().Add(time.Minute))
	if err != nil || cb.PluginID != "acme/b" {
		t.Fatalf("plugin B's backend must hold B's token, got %s (%v)", cb.PluginID, err)
	}
	// A's backend never holds B's token — delivery is per-URL, and there is no pull
	// path by which A could request one scoped to B.
	if ca.PluginID == "acme/b" {
		t.Fatal("cross-delivery: A's backend must never hold B's token")
	}
}
