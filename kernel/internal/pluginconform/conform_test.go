package pluginconform

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/m7mdhka/llmobs/kernel/internal/plugintest"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// firstPartyManifests locates every first-party plugin manifest in the repo.
func firstPartyManifests(t *testing.T) map[string]string {
	t.Helper()
	// repo root is three dirs up from kernel/internal/pluginconform.
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	pluginsDir := filepath.Join(root, "plugins")
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		t.Skipf("plugins dir not found: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(pluginsDir, e.Name(), "llmobs-plugin.yaml")
		if _, err := os.Stat(p); err == nil {
			out[e.Name()] = p
		}
	}
	return out
}

// TestFirstPartyPluginsConform: every first-party plugin's manifest passes static
// conformance. This is the CI bar — the dogfood rule means our own plugins meet
// the contract we ask third parties to meet.
func TestFirstPartyPluginsConform(t *testing.T) {
	manifests := firstPartyManifests(t)
	if len(manifests) == 0 {
		t.Fatal("expected at least one first-party plugin manifest")
	}
	for name, path := range manifests {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			rep, m := CheckManifest(raw)
			for _, r := range rep.Results {
				if !r.Pass {
					t.Errorf("%s: check %q failed: %s", name, r.Check, r.Detail)
				}
			}
			if m == nil {
				t.Fatalf("%s: manifest did not parse", name)
			}
		})
	}
}

// TestLiveConformanceAgainstBackend: the live checks (handshake + health +
// capability-within-grant) pass for a conformant backend, and a capability
// over-reach is caught.
func TestLiveConformanceAgainstBackend(t *testing.T) {
	m := &Manifest{}
	m.APIVersion = "llmobs.dev/v1alpha1"
	m.Kind = "Plugin"
	m.Metadata.ID = "acme/widget"
	m.Metadata.Version = "1.0.0"
	m.Spec.Capabilities = []string{"ingest", "query"}

	// Conformant backend.
	ok := plugintest.NewFakeBackend(pluginproto.Info{ID: "acme/widget", Version: "1.0.0", PluginAPIVersion: "v1alpha1", Capabilities: []string{"ingest"}})
	defer ok.Close()
	if rep := CheckLive(context.Background(), ok.URL(), m, nil); !rep.OK() {
		t.Fatalf("conformant backend should pass live checks: %+v", rep.Results)
	}

	// Over-reaching backend: echoes a capability the manifest didn't grant.
	bad := plugintest.NewFakeBackend(pluginproto.Info{ID: "acme/widget", Version: "1.0.0", PluginAPIVersion: "v1alpha1", Capabilities: []string{"ingest", "secrets"}})
	defer bad.Close()
	rep := CheckLive(context.Background(), bad.URL(), m, nil)
	if rep.OK() {
		t.Fatal("capability over-reach must fail conformance")
	}
	// The specific failing check is capability-within-grant.
	var found bool
	for _, r := range rep.Results {
		if r.Check == "capability-within-grant" && !r.Pass {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected capability-within-grant to fail: %+v", rep.Results)
	}
}

// TestBadManifestFails: a manifest with a bad id/capability is flagged.
func TestBadManifestFails(t *testing.T) {
	raw := []byte(`
apiVersion: llmobs.dev/v1alpha1
kind: Plugin
metadata:
  id: Bad_Id
  name: X
  version: not-semver
spec:
  capabilities: [ingest, bogus]
`)
	rep, _ := CheckManifest(raw)
	fails := map[string]bool{}
	for _, r := range rep.Results {
		if !r.Pass {
			fails[r.Check] = true
		}
	}
	for _, want := range []string{"id-format", "version-semver", "capabilities-known"} {
		if !fails[want] {
			t.Errorf("expected %q to fail", want)
		}
	}
}
