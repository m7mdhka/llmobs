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
	// Expected files exist. The offline contract check (backend/interop_test.py) and its data
	// (testdata/golden_vector.json) MUST be scaffolded — the CLI's "Next:" step tells the author
	// to run `python3 backend/interop_test.py`, so omitting it breaks the literal next command a
	// plugin author runs (a first-run hard stop). .gitignore keeps __pycache__ out of their repo.
	for _, f := range []string{
		"llmobs-plugin.yaml", "backend/app.py", "backend/llmobs_plugin.py",
		"backend/requirements.txt", "backend/Dockerfile", "README.md",
		"backend/interop_test.py", "backend/testdata/golden_vector.json", "backend/.gitignore",
	} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("missing scaffolded file %s: %v", f, err)
		}
	}
	// The scaffolded interop check must be the real, runnable contract test (not a stub) — it
	// imports the scaffolded llmobs_plugin and its golden vector.
	it, _ := os.ReadFile(filepath.Join(root, "backend", "interop_test.py"))
	if !strings.Contains(string(it), "verify_assertion") || !strings.Contains(string(it), "golden_vector") {
		t.Fatalf("interop_test.py is not the real contract check")
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
