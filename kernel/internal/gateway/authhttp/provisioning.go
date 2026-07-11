package authhttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
)

// Provisioning endpoints (Arc O / O3) — the arc's most dangerous surface: creating orgs,
// inviting/promoting/removing members. The whole design is one rule stated three ways:
//
//  1. ONE shared gate. Every MEMBER-provisioning sibling (invite, set-role, remove) enters
//     through orgProvisioner FIRST — no sibling is reachable with less, and none is strictly
//     more powerful than another. A new sibling added here MUST call it too (invariant #11).
//  2. Resolve against the TARGET org. orgProvisioner resolves the actor's role in the org the
//     action TARGETS (the {org} path segment), never an ambient/default org. This is the O2
//     plugin-settings bug's family (a gate reading the wrong tenant); provisioning is where it
//     is most dangerous, so it is closed by construction here.
//  3. Strictly below your own (perm.RoleAbove). A principal assigns only roles strictly below
//     their own and touches only members strictly below their own — the crown-jewel rule that
//     stops a provisioning bug from becoming account-takeover. Owners are therefore immutable
//     via provisioning (nothing outranks an owner), which is the last-owner protection stated
//     structurally: the org can never be stripped of its owner. Ownership transfer is a
//     deliberate future owner-only flow, not an oversight.
//
// create-org has NO target org (it creates one), so it cannot use orgProvisioner; it gates on
// instanceAdmin (an explicit instance-level authority) instead, and this file documents why.
//
// The actor is ALWAYS the server-resolved session (SessionFrom) — a client-supplied actor id
// is never read. Identity is never self-asserted.

// RegisterProvisioning mounts the org + membership provisioning endpoints. All are
// session-authenticated and CSRF-protected on mutating methods (via RequireAuth).
func (h *Handler) RegisterProvisioning(mux *http.ServeMux) {
	mux.Handle("/v1alpha1/orgs", h.RequireAuth(http.HandlerFunc(h.orgsCollection)))
	mux.Handle("/v1alpha1/orgs/", h.RequireAuth(http.HandlerFunc(h.orgsSub)))
}

// orgProvisioner is THE shared authorization gate for member provisioning. It resolves the
// actor's role in the TARGET org (server-derived session, never a client claim) and requires
// members:manage there. Returns the actor's role for the per-sibling strictly-below cap.
// A non-member of the target org, or a member without members:manage, is denied — no matter
// what authority they hold in any other org.
func (h *Handler) orgProvisioner(r *http.Request, orgID string) (actorID, actorRole string, ok bool) {
	sess, sok := SessionFrom(r.Context())
	if !sok {
		return "", "", false
	}
	role, err := controlplane.RoleInOrg(r.Context(), h.pool, sess.User.ID, orgID)
	if err != nil || !perm.Has(perm.RoleScopes(role), perm.MembersManage) {
		return "", "", false
	}
	return sess.User.ID, role, true
}

// orgsCollection: GET /v1alpha1/orgs (the caller's own memberships) | POST (create-org).
func (h *Handler) orgsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		sess, _ := SessionFrom(r.Context())
		ms, err := controlplane.MembershipsForUser(r.Context(), h.pool, sess.User.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "list failed")
			return
		}
		out := make([]map[string]string, 0, len(ms))
		for _, m := range ms {
			out = append(out, map[string]string{"org_id": m.OrgID, "role": m.Role})
		}
		writeJSON(w, http.StatusOK, map[string]any{"orgs": out})
	case http.MethodPost:
		// create-org has no target org to resolve against — it gates on instance-level
		// authority (default-org owner). The creator becomes the new org's owner.
		if _, ok := h.instanceAdmin(r); !ok {
			writeErr(w, http.StatusForbidden, "forbidden", "creating an organization requires instance-admin (an owner of the default org)")
			return
		}
		sess, _ := SessionFrom(r.Context())
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "schema_invalid", "invalid JSON")
			return
		}
		org, err := controlplane.CreateOrg(r.Context(), h.pool, req.Name, sess.User.ID)
		if err != nil {
			if strings.Contains(err.Error(), "name is required") {
				writeErr(w, http.StatusBadRequest, "schema_invalid", "org name is required")
				return
			}
			writeErr(w, http.StatusInternalServerError, "internal", "create failed")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"org_id": org.ID, "name": org.Name, "role": perm.RoleOwner})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST")
	}
}

// orgsSub routes /v1alpha1/orgs/{org}/members and /v1alpha1/orgs/{org}/members/{userId}.
func (h *Handler) orgsSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1alpha1/orgs/")
	parts := strings.Split(rest, "/")
	// Expect {org}/members  or  {org}/members/{userId}.
	if len(parts) < 2 || parts[0] == "" || parts[1] != "members" {
		writeErr(w, http.StatusNotFound, "not_found", "unknown org resource")
		return
	}
	orgID := parts[0]
	switch {
	case len(parts) == 2: // /{org}/members
		if r.Method == http.MethodPost {
			h.inviteMember(w, r, orgID)
			return
		}
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST to invite a member")
	case len(parts) == 3 && parts[2] != "": // /{org}/members/{userId}
		targetUserID := parts[2]
		switch r.Method {
		case http.MethodPut:
			h.setRole(w, r, orgID, targetUserID)
		case http.MethodDelete:
			h.removeMember(w, r, orgID, targetUserID)
		default:
			writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "PUT or DELETE")
		}
	default:
		writeErr(w, http.StatusNotFound, "not_found", "unknown org resource")
	}
}

// inviteMember: POST /v1alpha1/orgs/{org}/members {email, role, initial_password?}.
func (h *Handler) inviteMember(w http.ResponseWriter, r *http.Request, orgID string) {
	_, actorRole, ok := h.orgProvisioner(r, orgID)
	if !ok {
		writeErr(w, http.StatusForbidden, "forbidden", "inviting members requires members:manage in this org")
		return
	}
	var req struct {
		Email           string `json:"email"`
		Role            string `json:"role"`
		InitialPassword string `json:"initial_password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "schema_invalid", "invalid JSON")
		return
	}
	if !perm.ValidRole(req.Role) {
		writeErr(w, http.StatusBadRequest, "schema_invalid", "role must be one of owner|admin|member|viewer")
		return
	}
	// Crown-jewel cap: assign only a role STRICTLY BELOW your own.
	if !perm.RoleAbove(actorRole, req.Role) {
		writeErr(w, http.StatusForbidden, "forbidden", "cannot assign a role at or above your own")
		return
	}
	userID, createdUser, err := controlplane.InviteMember(r.Context(), h.pool, orgID, req.Email, req.Role, req.InitialPassword)
	switch {
	case errors.Is(err, controlplane.ErrAlreadyMember):
		writeErr(w, http.StatusConflict, "conflict", "user is already a member of this org; use set-role")
		return
	case errors.Is(err, controlplane.ErrNewUserNoPassword):
		writeErr(w, http.StatusBadRequest, "schema_invalid", "initial_password is required to invite a new user")
		return
	case errors.Is(err, controlplane.ErrInvalidRole):
		writeErr(w, http.StatusBadRequest, "schema_invalid", "invalid role")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "internal", "invite failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user_id": userID, "role": req.Role, "created_user": createdUser})
}

// setRole: PUT /v1alpha1/orgs/{org}/members/{userId} {role}.
func (h *Handler) setRole(w http.ResponseWriter, r *http.Request, orgID, targetUserID string) {
	_, actorRole, ok := h.orgProvisioner(r, orgID)
	if !ok {
		writeErr(w, http.StatusForbidden, "forbidden", "changing roles requires members:manage in this org")
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "schema_invalid", "invalid JSON")
		return
	}
	if !perm.ValidRole(req.Role) {
		writeErr(w, http.StatusBadRequest, "schema_invalid", "role must be one of owner|admin|member|viewer")
		return
	}
	targetRole, err := controlplane.RoleInOrg(r.Context(), h.pool, targetUserID, orgID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "resolve target failed")
		return
	}
	if targetRole == "" {
		writeErr(w, http.StatusNotFound, "not_found", "user is not a member of this org")
		return
	}
	// Cannot MODIFY a member at or above your own level (protects peers, superiors, and —
	// since nothing outranks an owner — every owner: the structural last-owner protection).
	if !perm.RoleAbove(actorRole, targetRole) {
		writeErr(w, http.StatusForbidden, "forbidden", "cannot modify a member at or above your own role")
		return
	}
	// Cannot ASSIGN a role at or above your own (no self/other escalation, no lateral clone).
	if !perm.RoleAbove(actorRole, req.Role) {
		writeErr(w, http.StatusForbidden, "forbidden", "cannot assign a role at or above your own")
		return
	}
	if err := controlplane.SetMembership(r.Context(), h.pool, targetUserID, orgID, req.Role); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "set-role failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": targetUserID, "role": req.Role})
}

// removeMember: DELETE /v1alpha1/orgs/{org}/members/{userId}.
func (h *Handler) removeMember(w http.ResponseWriter, r *http.Request, orgID, targetUserID string) {
	_, actorRole, ok := h.orgProvisioner(r, orgID)
	if !ok {
		writeErr(w, http.StatusForbidden, "forbidden", "removing members requires members:manage in this org")
		return
	}
	targetRole, err := controlplane.RoleInOrg(r.Context(), h.pool, targetUserID, orgID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "resolve target failed")
		return
	}
	if targetRole == "" {
		writeErr(w, http.StatusNotFound, "not_found", "user is not a member of this org")
		return
	}
	// Cannot remove a member at or above your own level — so an owner is never removable via
	// provisioning (the last-owner protection, structurally).
	if !perm.RoleAbove(actorRole, targetRole) {
		writeErr(w, http.StatusForbidden, "forbidden", "cannot remove a member at or above your own role")
		return
	}
	if err := controlplane.RemoveMembership(r.Context(), h.pool, targetUserID, orgID); err != nil {
		if errors.Is(err, controlplane.ErrNotMember) {
			writeErr(w, http.StatusNotFound, "not_found", "user is not a member of this org")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal", "remove failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
