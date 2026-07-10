package pluginapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/m7mdhka/llmobs/kernel/internal/gateway/pluginauth"
	"github.com/m7mdhka/llmobs/kernel/internal/plugindata"
)

// StoreBackend is the plugin-store surface the endpoints need (R1 boundary).
type StoreBackend interface {
	Put(ctx context.Context, pluginID, projectID, collection, id string, record json.RawMessage) error
	Get(ctx context.Context, pluginID, projectID, collection, id string) (json.RawMessage, bool, error)
	Query(ctx context.Context, pluginID, projectID, collection string, q plugindata.Query) (rows []json.RawMessage, next string, err error)
	Delete(ctx context.Context, pluginID, projectID, collection, id string) error
}

// Store serves the `store` primitive, gated on cap:store. Every op is scoped to
// the caller's (plugin_id, project_id) — project_id comes from the verified
// assertion, NEVER the request body, so a plugin can never reach another tenant's
// rows (the cross-tenant isolation invariant). (H5)
type Store struct {
	authz *pluginauth.Authorizer
	store StoreBackend
}

func NewStore(authz *pluginauth.Authorizer, store StoreBackend) *Store {
	return &Store{authz: authz, store: store}
}

// Register mounts the store endpoints under prefix (e.g. /v1alpha1/plugin/store).
func (h *Store) Register(mux *http.ServeMux, prefix string) {
	mux.HandleFunc(prefix+"/put", h.handle(h.put))
	mux.HandleFunc(prefix+"/get", h.handle(h.get))
	mux.HandleFunc(prefix+"/query", h.handle(h.query))
	mux.HandleFunc(prefix+"/delete", h.handle(h.del))
}

func (h *Store) handle(fn func(http.ResponseWriter, *http.Request, pluginauth.Caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		caller, status, err := h.authz.Require(r, "store")
		if err != nil {
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		fn(w, r, caller)
	}
}

type storeReq struct {
	Collection string           `json:"collection"`
	ID         string           `json:"id,omitempty"`
	Record     json.RawMessage  `json:"record,omitempty"`
	Query      plugindata.Query `json:"query,omitempty"`
}

func (h *Store) decode(w http.ResponseWriter, r *http.Request) (storeReq, bool) {
	var req storeReq
	if err := decodeJSON(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return req, false
	}
	if req.Collection == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "collection required"})
		return req, false
	}
	return req, true
}

func (h *Store) put(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) {
	req, ok := h.decode(w, r)
	if !ok {
		return
	}
	if req.ID == "" || len(req.Record) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id and record required"})
		return
	}
	if err := h.store.Put(r.Context(), c.PluginID, c.ProjectID, req.Collection, req.ID, req.Record); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Store) get(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) {
	req, ok := h.decode(w, r)
	if !ok {
		return
	}
	doc, found, err := h.store.Get(r.Context(), c.PluginID, c.ProjectID, req.Collection, req.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "id": req.ID})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": req.ID, "record": doc})
}

func (h *Store) query(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) {
	req, ok := h.decode(w, r)
	if !ok {
		return
	}
	rows, next, err := h.store.Query(r.Context(), c.PluginID, c.ProjectID, req.Collection, req.Query)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if rows == nil {
		rows = []json.RawMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": rows, "cursor": next})
}

func (h *Store) del(w http.ResponseWriter, r *http.Request, c pluginauth.Caller) {
	req, ok := h.decode(w, r)
	if !ok {
		return
	}
	if err := h.store.Delete(r.Context(), c.PluginID, c.ProjectID, req.Collection, req.ID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
