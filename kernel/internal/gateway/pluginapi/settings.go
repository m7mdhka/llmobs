package pluginapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/m7mdhka/llmobs/kernel/internal/gateway/pluginauth"
	"github.com/m7mdhka/llmobs/kernel/internal/pluginsettings"
)

// SettingsStore is the persistence surface the settings endpoints need
// (interface-at-consumer). Backed by pluginsettings.Store.
type SettingsStore interface {
	Get(ctx context.Context, m *pluginsettings.Model, pluginID, projectID string) (pluginsettings.View, error)
	Set(ctx context.Context, m *pluginsettings.Model, pluginID, projectID string, incoming map[string]json.RawMessage) error
}

// SchemaSource resolves a plugin's raw settings JSON Schema by id (registry-backed),
// whether it is in custom (opaque-storage) mode, and whether the plugin has settings at
// all. A custom-mode plugin may carry no schema (no secrets), so `ok` is not tied to
// schema presence.
type SchemaSource func(pluginID string) (schema json.RawMessage, custom, ok bool)

// Settings serves the plugin settings endpoints (J2). Unlike kv/secrets/store, these
// authorize the J1 FRONTEND token (a pure-frontend plugin's only credential); the
// plugin id + project come from the token, never the body. Settings is `kv` made
// frontend-reachable and schema-aware (ADR-0024).
type Settings struct {
	authz  *pluginauth.Authorizer
	store  SettingsStore
	schema SchemaSource
	// canWrite reports whether the session user may administer settings IN THE PROJECT the
	// write targets (Arc O / O2). It takes the token-resolved projectID so authority is
	// checked against the user's role in THAT project's org — never an ambient default-org
	// role. The tenant of the write and the authority for it must be the same org.
	canWrite func(r *http.Request, projectID string) bool
}

// NewSettings builds the handler. canWrite gates state-changing writes on the
// caller's CONFIGURATION authority (settings are project-shared plugin config, so a
// write must not be allowed to a read-only viewer): the frontend token proves which
// plugin/tenant, and canWrite (session role) proves the caller may administer it —
// the same split the supervisor uses (admins today, #21 RBAC seam). Reads are open
// to any valid frontend token for the plugin. A nil canWrite denies all writes.
func NewSettings(authz *pluginauth.Authorizer, store SettingsStore, schema SchemaSource, canWrite func(r *http.Request, projectID string) bool) *Settings {
	if canWrite == nil {
		canWrite = func(*http.Request, string) bool { return false }
	}
	return &Settings{authz: authz, store: store, schema: schema, canWrite: canWrite}
}

// Register mounts get/set under prefix (e.g. /v1alpha1/plugin/settings).
func (h *Settings) Register(mux *http.ServeMux, prefix string) {
	mux.HandleFunc(prefix+"/get", h.handle(h.get))
	mux.HandleFunc(prefix+"/set", h.handle(h.set))
}

// handle wraps a settings op with method + frontend-token auth + schema resolution,
// passing the scoped caller and the parsed model.
func (h *Settings) handle(fn func(http.ResponseWriter, *http.Request, pluginauth.Caller, *pluginsettings.Model)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		caller, status, err := h.authz.RequireFrontend(r)
		if err != nil {
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		raw, custom, ok := h.schema(caller.PluginID)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "plugin declares no settings"})
			return
		}
		model, err := pluginsettings.ParseSchema(raw, custom)
		if err != nil {
			// The manifest passed JSON validity at load but the schema is outside the
			// supported subset — a plugin misconfiguration, not a caller error.
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "settings schema unsupported"})
			return
		}
		fn(w, r, caller, model)
	}
}

func (h *Settings) get(w http.ResponseWriter, r *http.Request, c pluginauth.Caller, m *pluginsettings.Model) {
	view, err := h.store.Get(r.Context(), m, c.PluginID, c.ProjectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "settings get failed"})
		return
	}
	writeJSON(w, http.StatusOK, view)
}

type settingsSetReq struct {
	Values map[string]json.RawMessage `json:"values"`
}

func (h *Settings) set(w http.ResponseWriter, r *http.Request, c pluginauth.Caller, m *pluginsettings.Model) {
	// Writing project-shared plugin config (and its secrets) requires configuration
	// authority IN THIS PROJECT'S ORG — a viewer (in this project's org) must not overwrite
	// it even if they are an admin elsewhere. The project comes from the token (c.ProjectID)
	// and the authority is resolved against THAT project's org, so tenant and authority
	// match (O2 — no ambient default-org role gates a cross-org write).
	if !h.canWrite(r, c.ProjectID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "settings write requires configuration authority"})
		return
	}
	var req settingsSetReq
	if err := decodeJSON(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	if req.Values == nil {
		req.Values = map[string]json.RawMessage{}
	}
	if err := h.store.Set(r.Context(), m, c.PluginID, c.ProjectID, req.Values); err != nil {
		if ve, ok := pluginsettings.AsValidationError(err); ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": ve.Error(), "field": ve.Field})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "settings set failed"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
