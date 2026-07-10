package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateScaffoldsConformantPlugin(t *testing.T) {
	dir := t.TempDir()
	root, err := Create("acme/my-plugin", "My Cool Plugin", dir)
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join(dir, "my-plugin") {
		t.Fatalf("unexpected root: %s", root)
	}
	// Expected files exist.
	for _, f := range []string{"llmobs-plugin.yaml", "backend/app.py", "backend/llmobs_plugin.py", "backend/requirements.txt", "backend/Dockerfile", "README.md"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("missing scaffolded file %s: %v", f, err)
		}
	}
	// Placeholders were substituted (no template markers, id present).
	manifest, _ := os.ReadFile(filepath.Join(root, "llmobs-plugin.yaml"))
	ms := string(manifest)
	if strings.Contains(ms, "{{") || strings.Contains(ms, "owner/plugin-name") {
		t.Fatalf("manifest not templated: %s", ms)
	}
	if !strings.Contains(ms, "id: acme/my-plugin") {
		t.Fatalf("manifest id not set: %s", ms)
	}
	readme, _ := os.ReadFile(filepath.Join(root, "README.md"))
	if !strings.Contains(string(readme), "My Cool Plugin") {
		t.Fatal("display name not substituted in README")
	}
	app, _ := os.ReadFile(filepath.Join(root, "backend", "app.py"))
	if !strings.Contains(string(app), `PLUGIN_ID = "acme/my-plugin"`) {
		t.Fatalf("app.py PLUGIN_ID not set")
	}
}

func TestCreateRejectsBadIDAndCollision(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create("BadID", "", dir); err == nil {
		t.Fatal("bad id must be rejected")
	}
	if _, err := Create("acme/dup", "", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Create("acme/dup", "", dir); err == nil {
		t.Fatal("existing dir must be refused (no overwrite)")
	}
}
