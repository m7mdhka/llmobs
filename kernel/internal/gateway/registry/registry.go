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
	// Capabilities + Permissions are the plugin's manifest grant — the plugin half
	// of the frontend-token intersection (J1). A frontend token minted for this
	// plugin is scoped to Permissions ∩ the calling user's session.
	Capabilities []string `json:"capabilities,omitempty"`
	Permissions  []string `json:"permissions,omitempty"`
	// SettingsSchema is the raw JSON Schema for the plugin's settings (J2), loaded
	// from the manifest's settingsSchema path. Present only when the plugin declares
	// one; the shell renders it as a settings tab and the kernel validates writes
	// against it. Served to the shell in the registry list.
	SettingsSchema json.RawMessage `json:"settingsSchema,omitempty"`
}

// Source supplies the currently-available plugins. D4 implements a manifest
// scanner; D2 uses the empty source.
type Source interface {
	Plugins() []Plugin
}

// GrantFor returns a plugin's declared capabilities + permissions by id, false if
// no such plugin. Used by the frontend-token mint to compute the plugin half of
// the intersection (J1).
func GrantFor(src Source, id string) (caps, perms []string, ok bool) {
	for _, p := range src.Plugins() {
		if p.ID == id {
			return p.Capabilities, p.Permissions, true
		}
	}
	return nil, nil, false
}

// SchemaFor returns a plugin's raw settings JSON Schema by id, false if the plugin
// is unknown or declares no settingsSchema. Used by the settings store (J2) to find
// the secret (writeOnly) fields and validate a settings write.
func SchemaFor(src Source, id string) (schema json.RawMessage, ok bool) {
	for _, p := range src.Plugins() {
		if p.ID == id {
			return p.SettingsSchema, len(p.SettingsSchema) > 0
		}
	}
	return nil, false
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

// assetServer is optionally implemented by a source that serves plugin bundles.
type assetServer interface {
	AssetHandler() http.Handler
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
	// Serve plugin frontend bundles when the source provides them. The trailing
	// slash makes this a subtree so /v1alpha1/registry/plugins/<key>/assets/* is
	// served, while the exact path above returns the JSON list.
	if as, ok := h.src.(assetServer); ok {
		mux.Handle("/v1alpha1/registry/plugins/", as.AssetHandler())
	}
}
