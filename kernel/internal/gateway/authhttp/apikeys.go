package authhttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
)

// RegisterKeys mounts the machine API-key management endpoints. They are
// admin-session-authenticated (browsers, via the shell) and CSRF-protected on
// mutating methods — a machine caller obtains its key here once, then uses the
// bearer on the Query/ingest APIs. Keys are project-scoped to the session's
// default project (single-project lite).
func (h *Handler) RegisterKeys(mux *http.ServeMux) {
	mux.Handle("/v1alpha1/api-keys", h.RequireAuth(http.HandlerFunc(h.apiKeysCollection)))
	mux.Handle("/v1alpha1/api-keys/", h.RequireAuth(http.HandlerFunc(h.apiKeyItem)))
}

func (h *Handler) apiKeysCollection(w http.ResponseWriter, r *http.Request) {
	projectID, err := controlplane.DefaultProjectID(r.Context(), h.pool)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "no project")
		return
	}
	switch r.Method {
	case http.MethodGet:
		keys, err := controlplane.ListAPIKeys(r.Context(), h.pool, projectID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "list failed")
			return
		}
		if keys == nil {
			keys = []controlplane.APIKeyInfo{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
	case http.MethodPost:
		// Minting a machine credential is a configuration WRITE — gate it on write authority
		// in THIS project's org, so a read-only viewer cannot mint a key at all.
		// Resolved against the key's project, never an ambient default-org role.
		sess, _ := SessionFrom(r.Context())
		if _, ok := h.writeAuthorityInProject(r, projectID); !ok {
			writeErr(w, http.StatusForbidden, "forbidden", "minting an API key requires configuration authority in this project")
			return
		}
		var req struct {
			Scopes []string `json:"scopes"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "schema_invalid", "invalid JSON")
			return
		}
		// Cap the minted scopes to the MINTER's own authority: a member
		// who holds no traces:write/traces:delete cannot mint an ingest/delete key and thereby
		// escalate past their own role. The role is resolved per-project and the cap is enforced
		// at the CreateAPIKey seam; the frontend token already caps the same way (DataPermsOnly).
		minterRole, rerr := controlplane.RoleForProject(r.Context(), h.pool, sess.User.ID, projectID)
		if rerr != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "resolve minter authority failed")
			return
		}
		secret, publicKey, err := controlplane.CreateAPIKey(r.Context(), h.pool, projectID, req.Scopes, sess.User.ID, perm.RoleScopes(minterRole))
		if errors.Is(err, controlplane.ErrScopeExceedsMinter) {
			writeErr(w, http.StatusForbidden, "forbidden", "a requested scope exceeds your own authority; you may only mint scopes you hold")
			return
		}
		if err == controlplane.ErrInvalidScope {
			writeErr(w, http.StatusBadRequest, "invalid_scope", "scopes must be a non-empty subset of ingest|query|scores:write|delete")
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "create failed")
			return
		}
		// The secret is shown exactly once.
		writeJSON(w, http.StatusCreated, map[string]any{"secret": secret, "public_key": publicKey, "scopes": req.Scopes})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST")
	}
}

func (h *Handler) apiKeyItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE only")
		return
	}
	publicKey := strings.TrimPrefix(r.URL.Path, "/v1alpha1/api-keys/")
	if publicKey == "" {
		writeErr(w, http.StatusBadRequest, "schema_invalid", "missing key id")
		return
	}
	projectID, err := controlplane.DefaultProjectID(r.Context(), h.pool)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "no project")
		return
	}
	// Revoking a credential is a configuration write — same gate as minting one.
	if _, ok := h.writeAuthorityInProject(r, projectID); !ok {
		writeErr(w, http.StatusForbidden, "forbidden", "revoking an API key requires configuration authority in this project")
		return
	}
	if err := controlplane.RevokeAPIKey(r.Context(), h.pool, projectID, publicKey); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "revoke failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
