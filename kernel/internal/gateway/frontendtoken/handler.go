// Package frontendtoken mints a per-plugin, per-session, short-TTL FRONTEND token
// (J1): the browser-side least-privilege credential for a plugin's frontend. Its
// scopes are the plugin's manifest grant ∩ the calling user's session ∩ project —
// the same intersection H3 computes for backends, pre-computed here because a
// frontend has no service token to intersect at the Query API.
//
// IMPORTANT — this is least-privilege-BY-DEFAULT for SDK-using plugins, NOT a hard
// boundary against a hostile frontend. Plugin frontends load into the shell's
// origin + JS realm (ADR-0004 Module Federation), so a malicious plugin can bypass
// the SDK and call the Query API with the ambient session cookie directly, getting
// the user's full session scope. The documented v1 trust model is therefore:
// frontend-only plugins are trusted-at-install (like a browser/IDE extension); a
// plugin that needs HARD confinement runs a backend (H3 confines those). Origin
// isolation (ADR-0004 amendment) is the future path to a real frontend boundary,
// built when an untrusted third-party frontend plugin is a real requirement.
package frontendtoken

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/authhttp"
	"github.com/m7mdhka/llmobs/kernel/internal/gateway/registry"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

const tokenTTL = 5 * time.Minute

// Handler serves POST /v1alpha1/plugin-frontend-token.
type Handler struct {
	signer  *plugintoken.Signer
	source  registry.Source
	project func(*http.Request) (string, error)
}

func New(signer *plugintoken.Signer, source registry.Source, project func(*http.Request) (string, error)) *Handler {
	return &Handler{signer: signer, source: source, project: project}
}

// Mint issues a frontend token for the requested plugin, scoped to the plugin's
// grant ∩ the session user ∩ the project. Session-authed: the caller must be a
// signed-in user (the shell mints on the plugin's behalf).
func (h *Handler) Mint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sess, ok := authhttp.SessionFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "authentication required"})
		return
	}
	var req struct {
		Plugin string `json:"plugin"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.Plugin == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "plugin required"})
		return
	}
	_, pluginPerms, found := registry.GrantFor(h.source, req.Plugin)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such plugin"})
		return
	}
	projectID, err := h.project(r)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "no project"})
		return
	}
	// The intersection: plugin manifest perms ∩ the user's session perms. The
	// plugin can never exceed its own grant OR the user's, and the project is the
	// session's (a plugin cannot request another tenant).
	// DataPermsOnly: strip management scopes (owner/admin hold them) so a frontend token
	// can never carry members:manage/org:manage — even if the manifest grant were somehow
	// to include one. Belt-and-suspenders with the admission-time strip on pluginPerms.
	userPerms := perm.DataPermsOnly(perm.RoleScopes(sess.User.Role))
	effective := pluginproto.Intersect(pluginPerms, userPerms)

	tok, claims, err := h.signer.MintFrontendToken(
		req.Plugin, sess.User.Email, projectID, "frontend:"+sess.User.Email, effective, time.Now(), tokenTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "mint failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expiresUnix": claims.Exp, "scopes": effective})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
