package pluginapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/m7mdhka/llmobs/kernel/internal/gateway/pluginauth"
)

// SecretStore is the encrypted-secret storage surface (interface-at-consumer). It
// only ever holds ciphertext — the handler seals before Set and opens after Get.
type SecretStore interface {
	SetEncrypted(ctx context.Context, pluginID, projectID, name string, ciphertext, nonce []byte) error
	GetEncrypted(ctx context.Context, pluginID, projectID, name string) (ciphertext, nonce []byte, found bool, err error)
	ListNames(ctx context.Context, pluginID, projectID string) ([]string, error)
	Delete(ctx context.Context, pluginID, projectID, name string) error
}

// Sealer is the envelope-encryption surface (secretbox).
type Sealer interface {
	Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
	Open(ciphertext, nonce []byte) ([]byte, error)
}

// Secrets serves the `secrets` primitive, gated on cap:secrets. The plaintext is
// NEVER persisted, NEVER logged, and returned by exactly ONE path: the owning
// plugin's authenticated delivery (get). Every other surface — list, kv, query,
// errors, logs — carries names/flags only.
type Secrets struct {
	authz *pluginauth.Authorizer
	store SecretStore
	box   Sealer
	log   *slog.Logger
}

func NewSecrets(authz *pluginauth.Authorizer, store SecretStore, box Sealer, log *slog.Logger) *Secrets {
	if log == nil {
		log = slog.Default()
	}
	return &Secrets{authz: authz, store: store, box: box, log: log}
}

// Register mounts the secrets endpoints under prefix (e.g. /v1alpha1/plugin/secrets).
func (h *Secrets) Register(mux *http.ServeMux, prefix string) {
	mux.HandleFunc(prefix+"/set", h.handle(h.set))
	mux.HandleFunc(prefix+"/get", h.handle(h.get))
	mux.HandleFunc(prefix+"/list", h.handle(h.list))
	mux.HandleFunc(prefix+"/delete", h.handle(h.del))
}

func (h *Secrets) handle(fn func(http.ResponseWriter, *http.Request, pluginauth.Caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		caller, status, err := h.authz.Require(r, "secrets")
		if err != nil {
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		fn(w, r, caller)
	}
}

type secretReq struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

func (h *Secrets) decode(w http.ResponseWriter, r *http.Request) (secretReq, bool) {
	var req secretReq
	if err := decodeJSON(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return req, false
	}
	return req, true
}

func (h *Secrets) set(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) {
	req, ok := h.decode(w, r)
	if !ok {
		return
	}
	if req.Name == "" || req.Value == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "name and value required"})
		return
	}
	ct, nonce, err := h.box.Seal([]byte(req.Value))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "seal failed"})
		return
	}
	if err := h.store.SetEncrypted(r.Context(), c.PluginID, c.ProjectID, req.Name, ct, nonce); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "secret store failed"})
		return
	}
	// Log by NAME only — never the value.
	h.log.Info("plugin secret set", "plugin_id", c.PluginID, "name", req.Name)
	w.WriteHeader(http.StatusNoContent)
}

// get is the ONLY path that returns a plaintext, and only to the owning plugin
// (authenticated by its double token, scoped to its own plugin+project). This is
// the delivery the plugin needs to use the secret (e.g. call a provider).
func (h *Secrets) get(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) {
	req, ok := h.decode(w, r)
	if !ok {
		return
	}
	ct, nonce, found, err := h.store.GetEncrypted(r.Context(), c.PluginID, c.ProjectID, req.Name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "secret fetch failed"})
		return
	}
	if !found {
		// Error names the secret, never a value (nothing to leak — it's absent).
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "name": req.Name})
		return
	}
	pt, err := h.box.Open(ct, nonce)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "secret decrypt failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": req.Name, "value": string(pt)})
}

func (h *Secrets) list(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) {
	names, err := h.store.ListNames(r.Context(), c.PluginID, c.ProjectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "secret list failed"})
		return
	}
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]any{"name": n, "set": true}) // names + flags, NEVER values
	}
	writeJSON(w, http.StatusOK, map[string]any{"secrets": out})
}

func (h *Secrets) del(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) {
	req, ok := h.decode(w, r)
	if !ok {
		return
	}
	if err := h.store.Delete(r.Context(), c.PluginID, c.ProjectID, req.Name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "secret delete failed"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
