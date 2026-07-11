-- Arc O / O5 (#21 SSO) — per-org OIDC provider config + group→role mapping.
-- The FIRST external trust boundary: identity asserted by someone else's IdP. Nothing here
-- is trusted until the callback fully verifies the IdP ID token (signature/iss/aud/exp/nonce)
-- and the browser state (CSRF). This table only stores the CONFIG needed to run + verify the
-- flow; the group→role map is the ONLY source of an SSO user's role (no fallback).

CREATE TABLE IF NOT EXISTS sso_providers (
    org_id                  TEXT PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    issuer                  TEXT    NOT NULL,          -- OIDC issuer URL (discovery base)
    client_id               TEXT    NOT NULL,
    client_secret_ct        BYTEA   NOT NULL,          -- secretbox ciphertext (never returned)
    client_secret_nonce     BYTEA   NOT NULL,
    group_claim             TEXT    NOT NULL DEFAULT 'groups',
    local_password_disabled BOOLEAN NOT NULL DEFAULT false,
    enabled                 BOOLEAN NOT NULL DEFAULT true,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- group → role. A user's SSO role is the HIGHEST mapped role among their verified groups.
-- Each role is capped strictly-below the configurer's own role at write time (the O3 escalation
-- cap applied to this fourth provisioning path), so SSO can never grant owner.
CREATE TABLE IF NOT EXISTS sso_group_roles (
    org_id     TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    group_name TEXT NOT NULL,
    role       TEXT NOT NULL,                          -- owner|admin|member|viewer (validated)
    PRIMARY KEY (org_id, group_name)
);

-- SSO-only users have NO local password. A NULL password_hash means "local login impossible"
-- (VerifyPassword denies it); the account authenticates only through the verified IdP.
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;
