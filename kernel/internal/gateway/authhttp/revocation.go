package authhttp

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
)

// User revocation endpoint. Revoking a user is a global account-disable —
// it denies EVERY credential in the user's derivation subtree (sessions, minted API keys, and
// still-live frontend tokens / identity assertions) immediately, and blocks re-login. It is
// therefore gated on instance-admin (an owner of the default org), the same explicit
// instance-level authority as create-org — not a per-org role. The actor is the server
// session; the target is the {userId} path segment (never a body-supplied identity).

// RegisterRevocation mounts POST /v1alpha1/users/{userId}/revoke.
func (h *Handler) RegisterRevocation(mux *http.ServeMux) {
	mux.Handle("/v1alpha1/users/", h.RequireAuth(http.HandlerFunc(h.usersSub)))
}

func (h *Handler) usersSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1alpha1/users/")
	parts := strings.Split(rest, "/")
	// Expect {userId}/revoke.
	if len(parts) != 2 || parts[0] == "" || parts[1] != "revoke" {
		writeErr(w, http.StatusNotFound, "not_found", "unknown user resource")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST to revoke a user")
		return
	}
	targetUserID := parts[0]
	actor, ok := h.instanceAdmin(r)
	if !ok {
		writeErr(w, http.StatusForbidden, "forbidden", "revoking a user requires instance-admin (an owner of the default org)")
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	// Body is optional; ignore a decode error on an empty/short body.
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req)
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "revoked by " + actor
	}
	if err := controlplane.RevokeUser(r.Context(), h.pool, targetUserID, reason); err != nil {
		if strings.Contains(err.Error(), "unknown user") {
			writeErr(w, http.StatusNotFound, "not_found", "unknown user")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal", "revoke failed")
		return
	}
	// The whole derivation subtree is now denied on the next request. 204: no body.
	w.WriteHeader(http.StatusNoContent)
}
