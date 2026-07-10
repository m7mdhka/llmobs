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
	// The manifest grant (J1) is populated so the frontend-token mint can look it up.
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
	// The settings schema (J2) is loaded from the manifest-relative path and served
	// by SchemaFor for the settings store.
	schema, ok := SchemaFor(ds, "acme/demo")
	if !ok || !strings.Contains(string(schema), "writeOnly") {
		t.Fatalf("SchemaFor must return the loaded settings schema, got ok=%v schema=%s", ok, schema)
	}
	if _, ok := SchemaFor(ds, "acme/nope"); ok {
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
