package controlplane

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
)

// OIDC/SSO (Arc O / O5, #21) — the FIRST external trust boundary. Everything here is invoked
// only AFTER the HTTP layer has fully verified the IdP assertion (signature/iss/aud/exp/nonce)
// and the browser state (CSRF). This file never verifies a token; it stores the per-org config
// and applies the group→role mapping + JIT provisioning that the verified claims drive. A role
// resolved here comes ONLY from a verified group→role entry — never a client-supplied claim,
// never a fallback/default.

// sealer is the minimal secret-encryption surface SSO needs (interface at consumer). Backed by
// *secretbox.Box. The OIDC client_secret is sealed at rest and only ever decrypted in memory to
// run the token exchange — never returned to a client.
type sealer interface {
	Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
	Open(ciphertext, nonce []byte) ([]byte, error)
}

// SSOProvider is one org's OIDC configuration. ClientSecret is plaintext ONLY in memory after
// GetSSOProvider decrypts it; it is never serialized to a client (the HTTP GET returns a
// redacted view).
type SSOProvider struct {
	OrgID                 string
	Issuer                string
	ClientID              string
	ClientSecret          string
	GroupClaim            string
	LocalPasswordDisabled bool
	Enabled               bool
	GroupRoles            map[string]string // IdP group name → role (owner|admin|member|viewer)
}

// ErrNoSSOProvider is returned when an org has no configured provider.
var ErrNoSSOProvider = fmt.Errorf("no SSO provider configured for org")

// ErrLocalAccountExists is returned when SSO tries to authenticate an email that already owns a
// LOCAL-password account. SSO owns only passwordless rows; a local account is never claimable via
// an external IdP (identity-ownership guard).
var ErrLocalAccountExists = fmt.Errorf("an account with this email uses local login")

// GetSSOProvider loads an org's provider WITH the decrypted client secret + group→role map.
// Callers use it to run/verify the OIDC flow. Returns ErrNoSSOProvider if none.
func GetSSOProvider(ctx context.Context, pool *pgxpool.Pool, box sealer, orgID string) (*SSOProvider, error) {
	p := &SSOProvider{OrgID: orgID, GroupRoles: map[string]string{}}
	var ct, nonce []byte
	err := pool.QueryRow(ctx,
		`SELECT issuer, client_id, client_secret_ct, client_secret_nonce, group_claim, local_password_disabled, enabled
		   FROM sso_providers WHERE org_id = $1`, orgID).
		Scan(&p.Issuer, &p.ClientID, &ct, &nonce, &p.GroupClaim, &p.LocalPasswordDisabled, &p.Enabled)
	if err == pgx.ErrNoRows {
		return nil, ErrNoSSOProvider
	}
	if err != nil {
		return nil, err
	}
	secret, err := box.Open(ct, nonce)
	if err != nil {
		return nil, fmt.Errorf("decrypt client secret: %w", err)
	}
	p.ClientSecret = string(secret)
	rows, err := pool.Query(ctx, `SELECT group_name, role FROM sso_group_roles WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var g, role string
		if err := rows.Scan(&g, &role); err != nil {
			return nil, err
		}
		p.GroupRoles[g] = role
	}
	return p, rows.Err()
}

// SSOProviderView is the redacted, secret-free view returned to a client (config UI + login
// discovery). It NEVER carries the client secret.
type SSOProviderView struct {
	OrgID                 string            `json:"org_id"`
	Issuer                string            `json:"issuer"`
	ClientID              string            `json:"client_id"`
	GroupClaim            string            `json:"group_claim"`
	LocalPasswordDisabled bool              `json:"local_password_disabled"`
	Enabled               bool              `json:"enabled"`
	GroupRoles            map[string]string `json:"group_roles"`
}

// GetSSOProviderView returns the secret-free config for display. ErrNoSSOProvider if none.
func GetSSOProviderView(ctx context.Context, pool *pgxpool.Pool, orgID string) (*SSOProviderView, error) {
	v := &SSOProviderView{OrgID: orgID, GroupRoles: map[string]string{}}
	err := pool.QueryRow(ctx,
		`SELECT issuer, client_id, group_claim, local_password_disabled, enabled
		   FROM sso_providers WHERE org_id = $1`, orgID).
		Scan(&v.Issuer, &v.ClientID, &v.GroupClaim, &v.LocalPasswordDisabled, &v.Enabled)
	if err == pgx.ErrNoRows {
		return nil, ErrNoSSOProvider
	}
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `SELECT group_name, role FROM sso_group_roles WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var g, role string
		if err := rows.Scan(&g, &role); err != nil {
			return nil, err
		}
		v.GroupRoles[g] = role
	}
	return v, rows.Err()
}

// SetSSOProvider upserts an org's provider and REPLACES its group→role map, atomically. The
// client secret is sealed at rest. The caller (HTTP handler) is the authorization gate and has
// ALREADY capped every mapped role strictly-below the configurer's own role (the O3 escalation
// cap on this fourth provisioning path); this validates ValidRole as defense-in-depth.
func SetSSOProvider(ctx context.Context, pool *pgxpool.Pool, box sealer, p *SSOProvider) error {
	if strings.TrimSpace(p.Issuer) == "" || strings.TrimSpace(p.ClientID) == "" {
		return fmt.Errorf("issuer and client_id are required")
	}
	for g, role := range p.GroupRoles {
		if !perm.ValidRole(role) {
			return fmt.Errorf("group %q maps to invalid role %q", g, role)
		}
	}
	groupClaim := strings.TrimSpace(p.GroupClaim)
	if groupClaim == "" {
		groupClaim = "groups"
	}
	ct, nonce, err := box.Seal([]byte(p.ClientSecret))
	if err != nil {
		return fmt.Errorf("seal client secret: %w", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx,
		`INSERT INTO sso_providers (org_id, issuer, client_id, client_secret_ct, client_secret_nonce, group_claim, local_password_disabled, enabled, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8, now())
		 ON CONFLICT (org_id) DO UPDATE SET
		   issuer=EXCLUDED.issuer, client_id=EXCLUDED.client_id,
		   client_secret_ct=EXCLUDED.client_secret_ct, client_secret_nonce=EXCLUDED.client_secret_nonce,
		   group_claim=EXCLUDED.group_claim, local_password_disabled=EXCLUDED.local_password_disabled,
		   enabled=EXCLUDED.enabled, updated_at=now()`,
		p.OrgID, p.Issuer, p.ClientID, ct, nonce, groupClaim, p.LocalPasswordDisabled, p.Enabled); err != nil {
		return fmt.Errorf("upsert sso provider: %w", err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM sso_group_roles WHERE org_id = $1`, p.OrgID); err != nil {
		return err
	}
	for g, role := range p.GroupRoles {
		if _, err = tx.Exec(ctx,
			`INSERT INTO sso_group_roles (org_id, group_name, role) VALUES ($1,$2,$3)`,
			p.OrgID, g, role); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// EnabledSSOOrg is one org that offers SSO login (for the pre-login discovery affordance).
type EnabledSSOOrg struct {
	OrgID  string `json:"org_id"`
	Name   string `json:"name"`
	Issuer string `json:"issuer"`
}

// ListEnabledSSOOrgs returns orgs with an enabled provider, for the unauthenticated login page
// to render a "Sign in with SSO" button. It exposes only org id/name/issuer — no secret.
func ListEnabledSSOOrgs(ctx context.Context, pool *pgxpool.Pool) ([]EnabledSSOOrg, error) {
	rows, err := pool.Query(ctx,
		`SELECT s.org_id, o.name, s.issuer FROM sso_providers s
		    JOIN organizations o ON o.id = s.org_id
		   WHERE s.enabled = true ORDER BY o.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EnabledSSOOrg
	for rows.Next() {
		var e EnabledSSOOrg
		if err := rows.Scan(&e.OrgID, &e.Name, &e.Issuer); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SSORoleForGroups resolves an SSO user's role from their VERIFIED IdP groups: the HIGHEST role
// among the mapped groups. A user in NO mapped group returns "" → NO access (fail closed, not a
// fallback role). This is the ONLY place an SSO role is decided, and only from verified input.
func SSORoleForGroups(groupRoles map[string]string, userGroups []string) string {
	best := ""
	for _, g := range userGroups {
		role, ok := groupRoles[g]
		if !ok || !perm.ValidRole(role) {
			continue
		}
		if best == "" || perm.RoleAbove(role, best) {
			best = role
		}
	}
	return best
}

// JITProvisionSSOUser find-or-creates a passwordless user by email and sets their membership in
// the TARGET org to the mapped role (Arc O / O5). It goes THROUGH the O3 discipline, not around
// it: role only into orgID (target-tenant); the role is the group-map result, already capped
// strictly-below the configurer at config time (so it is never owner); and an existing OWNER is
// NEVER downgraded or altered by SSO (owners are locally-managed break-glass accounts — SSO can
// neither mint nor remove them). One transaction: a user is never left without its membership.
func JITProvisionSSOUser(ctx context.Context, pool *pgxpool.Pool, orgID, email, role string) (string, error) {
	if role == "" || !perm.ValidRole(role) {
		return "", ErrInvalidRole
	}
	if role == perm.RoleOwner {
		// Defense in depth: the config cap already forbids mapping a group to owner; refuse it
		// here too so no SSO path can ever mint an owner.
		return "", fmt.Errorf("SSO may not provision the owner role")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", fmt.Errorf("SSO assertion carried no email")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var uid string
	var existingHash *string
	qerr := tx.QueryRow(ctx, `SELECT id, password_hash FROM users WHERE lower(email) = lower($1)`, email).Scan(&uid, &existingHash)
	// IDENTITY-OWNERSHIP GUARD (the boundary review's CRITICAL): SSO must NEVER authenticate an
	// account that has a LOCAL password. Linking an IdP-asserted email to a local account would
	// let an org-controlled IdP claim (and take over) a locally-managed account — including the
	// break-glass owner. SSO owns only passwordless (SSO-provisioned) rows; a pre-existing
	// local-password account with this email is off-limits, and the login is refused.
	if qerr == nil && existingHash != nil {
		return "", ErrLocalAccountExists
	}
	switch {
	case qerr == pgx.ErrNoRows:
		nid, ierr := randomID("usr")
		if ierr != nil {
			return "", ierr
		}
		// Passwordless (NULL password_hash) — the account authenticates only via the verified
		// IdP. users.role is the legacy flat column (unread for authz); seed least-privilege.
		if _, err = tx.Exec(ctx,
			`INSERT INTO users (id, email, password_hash, role) VALUES ($1,$2,NULL,'viewer')`,
			nid, email); err != nil {
			return "", fmt.Errorf("create sso user: %w", err)
		}
		uid = nid
	case qerr != nil:
		return "", qerr
	}

	// Never let SSO change an existing owner (last-owner protection through the SSO path).
	var existing string
	merr := tx.QueryRow(ctx, `SELECT role FROM org_memberships WHERE user_id=$1 AND org_id=$2`, uid, orgID).Scan(&existing)
	if merr != nil && merr != pgx.ErrNoRows {
		return "", merr
	}
	if existing == perm.RoleOwner {
		if err = tx.Commit(ctx); err != nil {
			return "", err
		}
		return uid, nil
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO org_memberships (user_id, org_id, role) VALUES ($1,$2,$3)
		 ON CONFLICT (user_id, org_id) DO UPDATE SET role = EXCLUDED.role`,
		uid, orgID, role); err != nil {
		return "", fmt.Errorf("sync sso membership: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return uid, nil
}

// CountLocalOwners returns how many owners of an org still have a usable local password — the
// break-glass accounts. Used to refuse disabling local passwords if it would leave the org with
// no owner who can log in without the IdP (last-owner lockout protection).
func CountLocalOwners(ctx context.Context, pool *pgxpool.Pool, orgID string) (int, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM org_memberships m
		    JOIN users u ON u.id = m.user_id
		   WHERE m.org_id = $1 AND m.role = $2 AND u.password_hash IS NOT NULL`,
		orgID, perm.RoleOwner).Scan(&n)
	return n, err
}
