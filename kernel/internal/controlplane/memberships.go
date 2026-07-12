package controlplane

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
)

// Membership is a user's role in one org. A user's authority is per-org,
// resolved server-side from org_memberships — never a client-supplied claim.
type Membership struct {
	OrgID string
	Role  string
}

// DefaultOrgID returns the org that owns the earliest project — the single org in the
// lite profile, and the resolution point session→org routes through until multi-org
// project selection lands (mirrors DefaultProjectID).
func DefaultOrgID(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	var id string
	err := pool.QueryRow(ctx,
		`SELECT org_id FROM projects ORDER BY created_at ASC LIMIT 1`).Scan(&id)
	return id, err
}

// OrgForProject returns the org that owns a project. The authorization seam uses this to
// resolve which membership role governs a request scoped to a given project.
func OrgForProject(ctx context.Context, pool *pgxpool.Pool, projectID string) (string, error) {
	var orgID string
	err := pool.QueryRow(ctx, `SELECT org_id FROM projects WHERE id = $1`, projectID).Scan(&orgID)
	return orgID, err
}

// RoleForProject resolves a user's membership role in the org that OWNS a project — the
// per-request-per-project authority the auth convergence seam uses. Authority is always
// checked against the org that owns the target project, never the actor's ambient/default
// org. One query: project → its org → the user's membership role there. Returns "" when the project
// is unknown OR the user is not a member of its org → perm.RoleScopes("") = no scopes
// (FAIL CLOSED). This is what makes a user who is owner in org A but viewer in org B get
// VIEWER scope on org B's project — never their ambient default-org role.
func RoleForProject(ctx context.Context, pool *pgxpool.Pool, userID, projectID string) (string, error) {
	var role string
	err := pool.QueryRow(ctx,
		`SELECT COALESCE(m.role, '')
		   FROM projects p
		   LEFT JOIN org_memberships m ON m.org_id = p.org_id AND m.user_id = $1
		  WHERE p.id = $2`, userID, projectID).Scan(&role)
	if err == pgx.ErrNoRows {
		return "", nil // unknown project → no authority (fail closed)
	}
	return role, err
}

// RoleInOrg returns a user's membership role in an org, or "" if they are not a member.
// An empty role resolves (via perm.RoleScopes) to NO scopes — a user has zero authority
// in an org they don't belong to (the cross-org prove-the-negative).
func RoleInOrg(ctx context.Context, pool *pgxpool.Pool, userID, orgID string) (string, error) {
	var role string
	err := pool.QueryRow(ctx,
		`SELECT role FROM org_memberships WHERE user_id = $1 AND org_id = $2`, userID, orgID).Scan(&role)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return role, err
}

// MembershipsForUser returns all of a user's org memberships (for /me and, later,
// multi-org selection). Ordered by org for stable output.
func MembershipsForUser(ctx context.Context, pool *pgxpool.Pool, userID string) ([]Membership, error) {
	rows, err := pool.Query(ctx,
		`SELECT org_id, role FROM org_memberships WHERE user_id = $1 ORDER BY org_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.OrgID, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetMembership upserts a user's role in an org. Used by BootstrapAdmin (the first owner)
// and by provisioning. The role MUST be valid (perm.ValidRole) — the caller is the
// authorization gate; this is the persistence.
func SetMembership(ctx context.Context, pool *pgxpool.Pool, userID, orgID, role string) error {
	if !perm.ValidRole(role) {
		return ErrInvalidRole
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO org_memberships (user_id, org_id, role) VALUES ($1,$2,$3)
		 ON CONFLICT (user_id, org_id) DO UPDATE SET role = EXCLUDED.role`,
		userID, orgID, role)
	return err
}

// ErrInvalidRole is returned when a role outside the defined set is used.
var ErrInvalidRole = errInvalidRole

type roleErr struct{}

func (roleErr) Error() string { return "invalid role" }

var errInvalidRole error = roleErr{}
