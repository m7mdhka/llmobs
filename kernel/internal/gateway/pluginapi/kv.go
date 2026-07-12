// Package pluginapi hosts the kernel-side HTTP endpoints for the plugin data
// primitives (kv now; secrets/store follow). Every endpoint authorizes the plugin
// double token via pluginauth, gates on the capability, and scopes storage to the
// caller's (plugin_id, project_id) — the plugin never touches the database.
package pluginapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/m7mdhka/llmobs/kernel/internal/gateway/pluginauth"
)

// KVStore is the storage surface the kv endpoints need (interface-at-consumer). userID is the
// per-user scope dimension: "" = project scope (shared), a user identity = per-user scope.
type KVStore interface {
	Get(ctx context.Context, pluginID, projectID, userID, key string) (json.RawMessage, bool, error)
	Set(ctx context.Context, pluginID, projectID, userID, key string, value json.RawMessage) error
	Delete(ctx context.Context, pluginID, projectID, userID, key string) error
	List(ctx context.Context, pluginID, projectID, userID, prefix string) ([]string, error)
}

// KV serves the `kv` primitive endpoints, gated on the kv capability.
type KV struct {
	authz *pluginauth.Authorizer
	store KVStore
}

func NewKV(authz *pluginauth.Authorizer, store KVStore) *KV {
	return &KV{authz: authz, store: store}
}

// Register mounts the kv endpoints under prefix (e.g. /v1alpha1/plugin/kv).
func (h *KV) Register(mux *http.ServeMux, prefix string) {
	mux.HandleFunc(prefix+"/get", h.handle(h.get))
	mux.HandleFunc(prefix+"/set", h.handle(h.set))
	mux.HandleFunc(prefix+"/delete", h.handle(h.del))
	mux.HandleFunc(prefix+"/list", h.handle(h.list))
}

// handle wraps a kv op with method + capability auth, passing the scoped caller.
func (h *KV) handle(fn func(http.ResponseWriter, *http.Request, pluginauth.Caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		caller, status, err := h.authz.Require(r, "kv")
		if err != nil {
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		fn(w, r, caller)
	}
}

type kvReq struct {
	Key    string          `json:"key"`
	Value  json.RawMessage `json:"value,omitempty"`
	Prefix string          `json:"prefix,omitempty"`
	// Scope is "project" (default — shared across the project's users) or "user" (per-user,
	// isolated). The user IDENTITY is never taken from the body — only the scope choice is —
	// so a plugin can select ITS user's private bucket but can never name another user's.
	Scope string `json:"scope,omitempty"`
}

// reservedPrefix marks kernel-internal kv keys (the settings document lives at "__settings__").
// The settings store shares the plugin_kv namespace at project scope, so the PUBLIC kv surface
// reserves the "__" prefix: a plugin cannot read, overwrite, delete, or list its own settings
// document through the raw kv primitive — closing that overlap at the one seam (defense in depth;
// impact was already bounded to a plugin's own project with secrets sealed).
const reservedPrefix = "__"

func reservedKey(key string) bool { return strings.HasPrefix(key, reservedPrefix) }

// scopeUserID resolves the storage user-scope from the request's scope choice + the VERIFIED
// caller identity (resolve against the acting user, never a client field). "" = project scope.
// "user" keys on the caller's Subject (the verified user email); a user-less credential (empty
// Subject) cannot use per-user scope.
func scopeUserID(c pluginauth.Caller, scope string) (userID string, ok bool) {
	switch scope {
	case "", "project":
		return "", true
	case "user":
		if c.Subject == "" {
			return "", false
		}
		return strings.ToLower(c.Subject), true
	default:
		return "", false
	}
}

func (h *KV) get(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) {
	req, ok := decode(w, r)
	if !ok {
		return
	}
	if reservedKey(req.Key) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "reserved key"})
		return
	}
	uid, ok := scopeUserID(c, req.Scope)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid scope for this caller"})
		return
	}
	v, found, err := h.store.Get(r.Context(), c.PluginID, c.ProjectID, uid, req.Key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "kv get failed"})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "key": req.Key})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": req.Key, "value": v})
}

func (h *KV) set(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) {
	req, ok := decode(w, r)
	if !ok {
		return
	}
	if req.Key == "" || len(req.Value) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "key and value required"})
		return
	}
	if reservedKey(req.Key) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "reserved key"})
		return
	}
	uid, ok := scopeUserID(c, req.Scope)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid scope for this caller"})
		return
	}
	if err := h.store.Set(r.Context(), c.PluginID, c.ProjectID, uid, req.Key, req.Value); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "kv set failed"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *KV) del(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) {
	req, ok := decode(w, r)
	if !ok {
		return
	}
	if reservedKey(req.Key) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "reserved key"})
		return
	}
	uid, ok := scopeUserID(c, req.Scope)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid scope for this caller"})
		return
	}
	if err := h.store.Delete(r.Context(), c.PluginID, c.ProjectID, uid, req.Key); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "kv delete failed"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *KV) list(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) {
	req, ok := decode(w, r)
	if !ok {
		return
	}
	uid, ok := scopeUserID(c, req.Scope)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid scope for this caller"})
		return
	}
	keys, err := h.store.List(r.Context(), c.PluginID, c.ProjectID, uid, req.Prefix)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "kv list failed"})
		return
	}
	// Never surface kernel-internal keys (the settings doc) through the public kv surface.
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if !reservedKey(k) {
			out = append(out, k)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func decode(w http.ResponseWriter, r *http.Request) (kvReq, bool) {
	var req kvReq
	if err := decodeJSON(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return req, false
	}
	return req, true
}

// decodeJSON reads a capped JSON body into out.
func decodeJSON(w http.ResponseWriter, r *http.Request, out any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(out)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
