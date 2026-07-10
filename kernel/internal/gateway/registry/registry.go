// Package registry serves the plugin registry read API the shell's remote loader
// consumes: for each installed plugin, its id, nav items, routes, MF remote URL,
// and integrity hash. D2 ships the read endpoint with an empty source (no plugin
// install machinery yet); D4 wires a manifest-scanning source behind the same
// Handler. The shell hardcodes no plugins — this endpoint is its only input.
package registry

import (
	"encoding/json"
	"net/http"
)

// NavEntry mirrors the shell's nav model (route path, label, optional section).
type NavEntry struct {
	Path    string `json:"path"`
	Label   string `json:"label"`
	Section string `json:"section,omitempty"`
}

// Plugin is one advertised plugin as the shell's loader consumes it.
type Plugin struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Version       string     `json:"version"`
	RemoteEntry   string     `json:"remoteEntry"`
	RemoteName    string     `json:"remoteName"`
	ExposedModule string     `json:"exposedModule"`
	Integrity     string     `json:"integrity,omitempty"`
	Nav           []NavEntry `json:"nav"`
}

// Source supplies the currently-available plugins. D4 implements a manifest
// scanner; D2 uses the empty source.
type Source interface {
	Plugins() []Plugin
}

// EmptySource advertises no plugins.
type EmptySource struct{}

func (EmptySource) Plugins() []Plugin { return nil }

// Handler serves GET /v1alpha1/registry/plugins.
type Handler struct {
	src Source
}

func NewHandler(src Source) *Handler {
	if src == nil {
		src = EmptySource{}
	}
	return &Handler{src: src}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/v1alpha1/registry/plugins", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		plugins := h.src.Plugins()
		if plugins == nil {
			plugins = []Plugin{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"plugins": plugins})
	})
}
