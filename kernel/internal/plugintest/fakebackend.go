// Package plugintest provides a fake plugin backend HTTP server implementing the
// plugin protocol (api/plugin/v1alpha1) — reused as a fixture across the Arc-H
// PRs to drive the supervisor, gateway proxy, and SDK primitives without a real
// plugin. It is a normal (non-_test) package so tests in other kernel packages
// can import it.
package plugintest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// FakeBackend is a controllable plugin backend: it serves /plugin/v1/info and
// /plugin/v1/health, and can be flipped down (unreachable), unhealthy, or given a
// stale watermark to exercise the supervisor state machine.
type FakeBackend struct {
	server *httptest.Server
	mu     sync.Mutex
	info   pluginproto.Info
	health pluginproto.Health
	down   bool
}

// NewFakeBackend starts a healthy fake backend advertising info.
func NewFakeBackend(info pluginproto.Info) *FakeBackend {
	f := &FakeBackend{
		info:   info,
		health: pluginproto.Health{Live: true, Ready: true},
	}
	mux := http.NewServeMux()
	mux.HandleFunc(pluginproto.DefaultInfoPath, f.handleInfo)
	mux.HandleFunc(pluginproto.DefaultHealthPath, f.handleHealth)
	// Catch-all echo: returns the path + received headers, so proxy tests can assert
	// the cookie was stripped and the identity assertion injected.
	mux.HandleFunc("/", f.handleEcho)
	f.server = httptest.NewServer(mux)
	return f
}

// URL is the base URL to register with the supervisor.
func (f *FakeBackend) URL() string { return f.server.URL }

// Close shuts the server down.
func (f *FakeBackend) Close() { f.server.Close() }

// SetDown makes the backend return 503 to all probes (simulated unreachable).
func (f *FakeBackend) SetDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

// SetHealth overrides the reported health.
func (f *FakeBackend) SetHealth(h pluginproto.Health) {
	f.mu.Lock()
	f.health = h
	f.mu.Unlock()
}

// SetInfo overrides the handshake info.
func (f *FakeBackend) SetInfo(info pluginproto.Info) {
	f.mu.Lock()
	f.info = info
	f.mu.Unlock()
}

func (f *FakeBackend) handleInfo(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, f.info)
}

func (f *FakeBackend) handleHealth(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, f.health)
}

func (f *FakeBackend) handleEcho(w http.ResponseWriter, r *http.Request) {
	headers := map[string]string{}
	for k := range r.Header {
		headers[k] = r.Header.Get(k)
	}
	writeJSON(w, map[string]any{"path": r.URL.Path, "headers": headers})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
