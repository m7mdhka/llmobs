package registry

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const manifest = `apiVersion: llmobs.dev/v1alpha1
kind: Plugin
metadata:
  id: acme/demo
  name: Demo
  version: 1.2.3
spec:
  capabilities: [query]
  permissions: [traces:read.metadata, scores:read]
  settingsSchema: settings.schema.json
  frontend:
    remoteName: demo
    exposedModule: ./plugin
    entry: remoteEntry.js
    nav:
      - path: /demo
        label: Demo
        section: Observe
`

func TestDirSourceScanAndServe(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "demo", "llmobs-plugin.yaml"), manifest)
	writeFile(t, filepath.Join(root, "demo", "dist", "remoteEntry.js"), "console.log('remote');")
	writeFile(t, filepath.Join(root, "demo", "settings.schema.json"), `{"type":"object","properties":{"apiKey":{"type":"string","writeOnly":true}}}`)
	// A dir with no manifest is skipped, not fatal.
	if err := os.MkdirAll(filepath.Join(root, "junk"), 0o755); err != nil {
		t.Fatal(err)
	}

	ds := NewDirSource(root, "/v1alpha1/registry/plugins", slog.New(slog.NewTextHandler(io.Discard, nil)))
	plugins := ds.Plugins()
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(plugins))
	}
	p := plugins[0]
	if p.ID != "acme/demo" || p.RemoteName != "demo" || p.Version != "1.2.3" {
		t.Fatalf("manifest not parsed: %+v", p)
	}
	if p.RemoteEntry != "/v1alpha1/registry/plugins/acme__demo/assets/remoteEntry.js" {
		t.Fatalf("remote entry url wrong: %s", p.RemoteEntry)
	}
	if len(p.Integrity) < 8 || p.Integrity[:7] != "sha384-" {
		t.Fatalf("expected sha384 integrity, got %q", p.Integrity)
	}
	if len(p.Nav) != 1 || p.Nav[0].Path != "/demo" {
		t.Fatalf("nav not parsed: %+v", p.Nav)
	}
	// The manifest grant is populated so the frontend-token mint can look it up.
	if len(p.Capabilities) != 1 || p.Capabilities[0] != "query" {
		t.Fatalf("capabilities not parsed: %+v", p.Capabilities)
	}
	if len(p.Permissions) != 2 || p.Permissions[0] != "traces:read.metadata" {
		t.Fatalf("permissions not parsed: %+v", p.Permissions)
	}
	caps, perms, ok := GrantFor(ds, "acme/demo")
	if !ok || len(caps) != 1 || len(perms) != 2 {
		t.Fatalf("GrantFor must return the manifest grant, got caps=%v perms=%v ok=%v", caps, perms, ok)
	}
	if _, _, ok := GrantFor(ds, "acme/nope"); ok {
		t.Fatal("GrantFor must report false for an unknown plugin")
	}
	// The settings schema is loaded from the manifest-relative path and served
	// by SchemaFor for the settings store.
	schema, _, ok := SchemaFor(ds, "acme/demo")
	if !ok || !strings.Contains(string(schema), "writeOnly") {
		t.Fatalf("SchemaFor must return the loaded settings schema, got ok=%v schema=%s", ok, schema)
	}
	if _, _, ok := SchemaFor(ds, "acme/nope"); ok {
		t.Fatal("SchemaFor must report false for an unknown plugin")
	}

	// The asset handler serves the bundle and refuses traversal.
	h := ds.AssetHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p.RemoteEntry, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("asset should be served, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1alpha1/registry/plugins/acme__demo/assets/../../../etc/passwd", nil))
	if rec.Code == http.StatusOK {
		t.Fatal("path traversal must not be served")
	}
}

// TestDirSourceDevRemotes: with a dev-remote override (`make dev`), the plugin's
// remoteEntry is the live dev-server URL and the integrity hash is dropped (the dev
// bundle changes every save). Plugins without an override are untouched.
func TestDirSourceDevRemotes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "demo", "llmobs-plugin.yaml"), manifest)
	writeFile(t, filepath.Join(root, "demo", "dist", "remoteEntry.js"), "console.log('remote');")
	writeFile(t, filepath.Join(root, "demo", "settings.schema.json"), `{"type":"object"}`)

	ds := NewDirSource(root, "/v1alpha1/registry/plugins", slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithDevRemotes(map[string]string{"acme/demo": "http://localhost:3001/remoteEntry.js"}))
	p := ds.Plugins()[0]
	if p.RemoteEntry != "http://localhost:3001/remoteEntry.js" {
		t.Fatalf("dev remote must override remoteEntry, got %s", p.RemoteEntry)
	}
	if p.Integrity != "" {
		t.Fatalf("dev remote must drop integrity, got %q", p.Integrity)
	}

	// A source without the override advertises the built dist + integrity.
	prod := NewDirSource(root, "/v1alpha1/registry/plugins", slog.New(slog.NewTextHandler(io.Discard, nil)))
	pp := prod.Plugins()[0]
	if pp.RemoteEntry == p.RemoteEntry || pp.Integrity == "" {
		t.Fatalf("without the override, remoteEntry must be the built dist with integrity: %+v", pp)
	}

	// The key dev scenario: a plugin with NO built dist still loads when it has a dev
	// remote (its frontend is served live by its own dev server). Without the override
	// the same dir fails (no dist to hash).
	nodist := t.TempDir()
	writeFile(t, filepath.Join(nodist, "demo", "llmobs-plugin.yaml"), manifest)
	writeFile(t, filepath.Join(nodist, "demo", "settings.schema.json"), `{"type":"object"}`)
	dev := NewDirSource(nodist, "/v1alpha1/registry/plugins", slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithDevRemotes(map[string]string{"acme/demo": "http://localhost:3001/remoteEntry.js"}))
	if len(dev.Plugins()) != 1 {
		t.Fatal("a dev-remote plugin must load without a built dist")
	}
	if none := NewDirSource(nodist, "/v1alpha1/registry/plugins", slog.New(slog.NewTextHandler(io.Discard, nil))); len(none.Plugins()) != 0 {
		t.Fatal("without a dev remote, a plugin with no dist must be skipped")
	}
}
