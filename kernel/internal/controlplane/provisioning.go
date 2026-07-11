package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
)

// Provisioning persistence (Arc O / O3). Every function here is PURE PERSISTENCE — the
// caller (the HTTP handler) is the authorization gate. Nothing in this file resolves or
// trusts an actor role; the handler resolves the actor's role against the TARGET org and
// enforces the strictly-below cap BEFORE calling in. Keeping authz out of persistence is
// deliberate: the one gate lives at the handler seam (invariant #11), not smeared across
// the data layer where a new caller could forget it.

// Org is a created organization (the create-org response).
type Org struct {
	ID   string
	Name string
}

var (
	// ErrAlreadyMember: invite targeted a user who already belongs to the org (use set-role).
	ErrAlreadyMember = errors.New("user is already a member of this org")
	// ErrNewUserNoPassword: inviting a NEW user (no existing account) without an initial
	// password. We never create a passwordless account (it could never authenticate) and we
	// never invent one silently.
	ErrNewUserNoPassword = errors.New("initial password required to invite a new user")
	// ErrNotMember: set-role/remove targeted a user who is not a member of the org.
	ErrNotMember = errors.New("user is not a member of this org")
)

// CreateOrg creates a new organization and grants its CREATOR the owner membership, in ONE
// transaction. A partial failure must never leave an org with no owner (an unadministrable
// tenant) — the org insert and the owner grant commit together or not at all. The caller
// (handler) has already checked the actor is authorized to create orgs (instance admin).
func CreateOrg(ctx context.Context, pool *pgxpool.Pool, name, creatorUserID string) (Org, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Org{}, fmt.Errorf("org name is required")
	}
	id, err := randomID("org")
	if err != nil {
		return Org{}, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Org{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `INSERT INTO organizations (id, name) VALUES ($1,$2)`, id, name); err != nil {
		return Org{}, fmt.Errorf("insert org: %w", err)
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO org_memberships (user_id, org_id, role) VALUES ($1,$2,$3)`,
		creatorUserID, id, perm.RoleOwner); err != nil {
		return Org{}, fmt.Errorf("grant creator owner membership: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return Org{}, fmt.Errorf("commit create-org: %w", err)
	}
	return Org{ID: id, Name: name}, nil
}

// InviteMember adds a user to an org with a role, in ONE transaction. If no account exists
// for the email it creates one (argon2id-hashed initialPassword required); if the account
// exists it is added as-is — invite NEVER resets an existing user's password (that would be
// an account-takeover path). Returns ErrAlreadyMember if the user already belongs to the
// org (the caller should use set-role). The role's validity and the strictly-below-actor
// cap are the HANDLER's responsibility (checked before this call); this asserts ValidRole
// only as a defense-in-depth persistence guard.
func InviteMember(ctx context.Context, pool *pgxpool.Pool, orgID, email, role, initialPassword string) (userID string, createdUser bool, err error) {
	if !perm.ValidRole(role) {
		return "", false, ErrInvalidRole
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return "", false, fmt.Errorf("email is required")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Resolve or create the user by email (case-insensitive, matching VerifyPassword).
	var uid string
	qerr := tx.QueryRow(ctx, `SELECT id FROM users WHERE lower(email) = lower($1)`, email).Scan(&uid)
	switch {
	case qerr == pgx.ErrNoRows:
		if strings.TrimSpace(initialPassword) == "" {
			return "", false, ErrNewUserNoPassword
		}
		hash, herr := hashArgon2id(initialPassword)
		if herr != nil {
			return "", false, fmt.Errorf("hash invitee password: %w", herr)
		}
		nid, ierr := randomID("usr")
		if ierr != nil {
			return "", false, ierr
		}
		// users.role is the LEGACY flat column (deprecated; membership is authoritative —
		// ResolveSession/VerifyPassword read org_memberships, never this). Seed it to viewer
		// (least privilege) so no legacy path that might still read it could over-grant.
		if _, err = tx.Exec(ctx,
			`INSERT INTO users (id, email, password_hash, role) VALUES ($1,$2,$3,'viewer')`,
			nid, email, hash); err != nil {
			return "", false, fmt.Errorf("create invitee: %w", err)
		}
		uid, createdUser = nid, true
	case qerr != nil:
		return "", false, qerr
	}

	// Already a member? Reject (use set-role) — invite must not silently change a role.
	var existing string
	merr := tx.QueryRow(ctx, `SELECT role FROM org_memberships WHERE user_id = $1 AND org_id = $2`, uid, orgID).Scan(&existing)
	if merr == nil {
		return "", false, ErrAlreadyMember
	}
	if merr != pgx.ErrNoRows {
		return "", false, merr
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO org_memberships (user_id, org_id, role) VALUES ($1,$2,$3)`,
		uid, orgID, role); err != nil {
		return "", false, fmt.Errorf("grant membership: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return "", false, fmt.Errorf("commit invite: %w", err)
	}
	return uid, createdUser, nil
}

// RemoveMembership deletes a user's membership in an org. Returns ErrNotMember if there was
// nothing to remove. The caller has already verified the actor outranks the target (the
// strictly-below cap), so an owner can never be removed here by a non-owner.
func RemoveMembership(ctx context.Context, pool *pgxpool.Pool, userID, orgID string) error {
	tag, err := pool.Exec(ctx, `DELETE FROM org_memberships WHERE user_id = $1 AND org_id = $2`, userID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotMember
	}
	return nil
}

// CountOwners returns how many owners an org has. Used for the explicit last-owner guard —
// though owners are already structurally unremovable via provisioning (nothing outranks an
// owner, so RoleAbove(actor, owner) is never true), this is the defense-in-depth belt that
// keeps the invariant if the rules ever change.
func CountOwners(ctx context.Context, pool *pgxpool.Pool, orgID string) (int, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM org_memberships WHERE org_id = $1 AND role = $2`, orgID, perm.RoleOwner).Scan(&n)
	return n, err
}
