// Package webui serves the built web shell (a static SPA) from the kernel so the
// lite profile is a single container: the browser hits the kernel origin for both
// the UI and the API. Assets come from a directory (LLMOBS_WEBUI_DIR, populated
// at image build time); when absent, a minimal placeholder is served so the
// kernel still runs headless. The shell is a pure MF host — the kernel serves its
// bytes but knows nothing about its plugins.
package webui

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Handler serves the SPA from dir with history-API fallback: unknown non-asset
// GET paths return index.html so client-side routes deep-link. If dir is empty or
// has no index.html, a placeholder page is served.
func Handler(dir string) http.Handler {
	index := filepath.Join(dir, "index.html")
	if dir == "" || !fileExists(index) {
		return http.HandlerFunc(placeholder)
	}
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		clean := filepath.Clean(r.URL.Path)
		// Serve a real file when it exists; otherwise fall back to index.html for
		// SPA client-side routing. Never fall back for asset-looking paths so a
		// missing bundle 404s honestly instead of returning HTML.
		if clean != "/" && fileExists(filepath.Join(dir, clean)) {
			fs.ServeHTTP(w, r)
			return
		}
		if looksLikeAsset(clean) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeFile(w, r, index)
	})
}

func looksLikeAsset(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	switch ext {
	case "", ".html":
		return false
	default:
		return true
	}
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func placeholder(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>LLMObs</title></head>` +
		`<body style="font-family:system-ui;background:#0d0e12;color:#e7e9ee;display:grid;place-items:center;height:100vh;margin:0">` +
		`<div style="text-align:center"><h1 style="font-weight:600">LLMObs</h1>` +
		`<p style="color:#9aa0ad">The web shell is not bundled in this build. Set LLMOBS_WEBUI_DIR to serve it.</p></div>` +
		`</body></html>`))
}
