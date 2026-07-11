package supervisor

import (
	"context"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/plugintest"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// TestDisableRevokesPluginTokens: disabling a RUNNING plugin fires the token-revoker hook (so
// its already-issued service token is denied at the next verify, not at TTL — O4/#63), and
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
