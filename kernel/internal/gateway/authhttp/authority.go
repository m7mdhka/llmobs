package authhttp

import (
	"context"
	"net/http"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
)

// Shared authorization seams (Arc O / O3). These are the ONE place each authority question
// is answered, so every caller inherits the invariant by construction (invariant #11) — a
// new handler asks the seam, it does not re-derive the rule. Two axes:
//
//   - writeAuthorityInProject: per-PROJECT configuration authority, resolved against the
//     user's role in THAT project's org (the O2 lesson: authority is checked against the
//     tenant the action targets, never an ambient default-org role).
//   - instanceAdmin: instance-LEVEL authority (an owner of the default org). Used only for
//     actions that are genuinely instance-wide — creating an org (no target org exists yet)
//     and editing the global price table (one table shared by every tenant). Naming this
//     axis explicitly is how O3 resolves the O2 pricing residual: a per-org role governs
//     per-org actions; only the default-org owner governs instance-wide ones.
//
// Both take the SERVER-DERIVED session actor (SessionFrom) — never a client-supplied actor
// id or role — and fail closed on any resolution error. The underlying resolver is
// injectable (Handler.instanceAdminFn/projectWriteFn) so the authz-matrix unit tests run
// without a DB; nil means the pool-backed default below.

// writeAuthorityInProject reports whether the session may perform a configuration WRITE in
// projectID, resolving the caller's role in that project's org. Returns an audit actor
// string ("session:<email>") alongside the decision.
func (h *Handler) writeAuthorityInProject(r *http.Request, projectID string) (actor string, ok bool) {
	sess, sok := SessionFrom(r.Context())
	if !sok {
		return "", false
	}
	allowed, err := h.resolveProjectWrite(r.Context(), sess.User.ID, projectID)
	if err != nil || !allowed {
		return "", false
	}
	return "session:" + sess.User.Email, true
}

// instanceAdmin reports whether the session is the instance super-admin — an OWNER of the
// default org (holds org:manage there). Returns an audit actor string alongside the
// decision. This is an EXPLICIT instance-level authority, not an ambient per-org role
// standing in for a global one.
func (h *Handler) instanceAdmin(r *http.Request) (actor string, ok bool) {
	sess, sok := SessionFrom(r.Context())
	if !sok {
		return "", false
	}
	allowed, err := h.resolveInstanceAdmin(r.Context(), sess.User.ID)
	if err != nil || !allowed {
		return "", false
	}
	return "session:" + sess.User.Email, true
}

func (h *Handler) resolveProjectWrite(ctx context.Context, userID, projectID string) (bool, error) {
	if h.projectWriteFn != nil {
		return h.projectWriteFn(ctx, userID, projectID)
	}
	role, err := controlplane.RoleForProject(ctx, h.pool, userID, projectID)
	if err != nil {
		return false, err
	}
	return perm.HasWriteAuthority(perm.RoleScopes(role)), nil
}

func (h *Handler) resolveInstanceAdmin(ctx context.Context, userID string) (bool, error) {
	if h.instanceAdminFn != nil {
		return h.instanceAdminFn(ctx, userID)
	}
	orgID, err := controlplane.DefaultOrgID(ctx, h.pool)
	if err != nil {
		return false, err
	}
	role, err := controlplane.RoleInOrg(ctx, h.pool, userID, orgID)
	if err != nil {
		return false, err
	}
	return perm.Has(perm.RoleScopes(role), perm.OrgManage), nil
}
