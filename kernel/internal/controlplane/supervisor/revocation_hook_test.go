package supervisor

import (
	"context"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/executors"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/platform/metrics"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/plugintest"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// TestDisableRevokesPluginTokens: disabling a RUNNING plugin fires the token-revoker hook (so
// its already-issued service token is denied at the next verify, not at TTL), and
// re-enabling fires the reinstate hook. Dropping the local rt.token alone would leave the
// issued token valid for its whole TTL, which is exactly the gap.
func TestDisableRevokesPluginTokens(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	fake := plugintest.NewFakeBackend(pluginproto.Info{
		ID: "acme/widget", Version: "1.0.0", PluginAPIVersion: "v1alpha1",
		Capabilities: []string{"ingest", "query"},
	})
	defer fake.Close()
	sup, _ := newSup(t, fake, 0, clk)

	var revoked, reinstated []string
	sup.SetTokenRevoker(
		func(id, _ string) { revoked = append(revoked, id) },
		func(id string) { reinstated = append(reinstated, id) },
	)

	// Bring it to running so it holds an issued service token.
	sup.Reconcile(ctx)
	if state(sup, "acme/widget") != StateRunning {
		t.Fatalf("want running, got %s", state(sup, "acme/widget"))
	}

	sup.Disable("acme/widget", "maintenance")
	if len(revoked) != 1 || revoked[0] != "acme/widget" {
		t.Fatalf("operator disable must revoke the plugin's issued tokens, got %v", revoked)
	}

	sup.Enable("acme/widget")
	if len(reinstated) != 1 || reinstated[0] != "acme/widget" {
		t.Fatalf("re-enable must reinstate the plugin's tokens, got %v", reinstated)
	}
}

// TestAutoDisableRevokesPluginTokens closes the security-review MEDIUM: a plugin AUTO-disabled
// by the fault cap (not an operator) must ALSO have its issued service token revoked — else a
// compromised plugin whose probes fail into auto-disable keeps a valid token for its full TTL
// while the operator sees "disabled". Both disable paths now funnel through toDisabled.
func TestAutoDisableRevokesPluginTokens(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	fake := plugintest.NewFakeBackend(pluginproto.Info{
		ID: "acme/widget", Version: "1.0.0", PluginAPIVersion: "v1alpha1",
		Capabilities: []string{"ingest", "query"},
	})
	defer fake.Close()
	sup, _ := newSup(t, fake, 0, clk)
	var revoked []string
	sup.SetTokenRevoker(func(id, _ string) { revoked = append(revoked, id) }, func(string) {})

	sup.Reconcile(ctx) // running, token issued
	// Drive the backend down until the fault cap auto-disables it.
	fake.SetDown(true)
	for i := 0; i < 6; i++ {
		clk.add(10 * time.Second)
		sup.Reconcile(ctx)
	}
	if state(sup, "acme/widget") != StateDisabled {
		t.Fatalf("want auto-disabled after the fault cap, got %s", state(sup, "acme/widget"))
	}
	if len(revoked) != 1 || revoked[0] != "acme/widget" {
		t.Fatalf("auto-disable must revoke the plugin's issued token, got %v", revoked)
	}
}

// mutableProvider lets a test add/remove plugins between reconciles (uninstall).
type mutableProvider struct{ specs []PluginSpec }

func (m *mutableProvider) Plugins() []PluginSpec { return m.specs }

// TestUninstallRevokesPluginTokens closes the security-review LOW: a plugin removed from the
// provider (uninstalled) must have its issued token revoked and its runtime forgotten — not
// left lingering with a token valid to TTL.
func TestUninstallRevokesPluginTokens(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	fake := plugintest.NewFakeBackend(pluginproto.Info{
		ID: "acme/widget", Version: "1.0.0", PluginAPIVersion: "v1alpha1",
		Capabilities: []string{"ingest", "query"},
	})
	defer fake.Close()
	prov := &mutableProvider{specs: []PluginSpec{{
		ID: "acme/widget", GrantedCapabilities: []string{"ingest", "query"},
		Backend: executors.Backend{URL: fake.URL()},
	}}}
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := New(prov, executors.NewExternalURL(2*time.Second), signer, metrics.New(), log,
		Config{BaseBackoff: time.Second, MaxBackoff: 4 * time.Second, MaxFaults: 3, TokenTTL: 10 * time.Minute}, clk.now)
	var revoked []string
	sup.SetTokenRevoker(func(id, _ string) { revoked = append(revoked, id) }, func(string) {})

	sup.Reconcile(ctx) // running, token issued
	if state(sup, "acme/widget") != StateRunning {
		t.Fatalf("want running, got %s", state(sup, "acme/widget"))
	}
	// Uninstall: drop it from the provider, reconcile.
	prov.specs = nil
	sup.Reconcile(ctx)
	if len(revoked) != 1 || revoked[0] != "acme/widget" {
		t.Fatalf("uninstall must revoke the plugin's issued token, got %v", revoked)
	}
	if state(sup, "acme/widget") != "" {
		t.Fatalf("uninstalled plugin must be forgotten, still present as %s", state(sup, "acme/widget"))
	}
}
