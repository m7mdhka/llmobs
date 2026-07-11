-- Real RBAC foundation (Arc O / O1). A user's authority is no longer a single flat
-- `users.role` string — it is a per-ORG membership role (owner|admin|member|viewer),
-- resolved server-side. This is the table users were always meant to link through
-- (0004 carried `users.role` as a placeholder "so RBAC slots in later").
--
-- Data-preserving upgrade: every EXISTING user is backfilled a membership in the default
-- org, mapping the legacy `users.role` — the bootstrap admin (role='admin') becomes the
-- org OWNER, anything unrecognized falls to the least-privileged `viewer` (fail-safe). A
-- fresh install has no users yet; BootstrapAdmin creates the first owner membership after
-- Bootstrap seeds the org. `users.role` is retained (unread) for rollback safety; a later
-- migration may drop it once the membership model is settled.
CREATE TABLE IF NOT EXISTS org_memberships (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id     TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    role       TEXT NOT NULL,   -- owner | admin | member | viewer (validated in app: perm.ValidRole)
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, org_id)
);
CREATE INDEX IF NOT EXISTS idx_org_memberships_org ON org_memberships(org_id);

-- Backfill existing users into the default org (the earliest project's org), preserving
-- their effective authority. No-op on a fresh DB (no users / no projects yet). NOTE: the
-- CROSS JOIN LATERAL yields no rows if there are users but NO project/org — those users
-- would get no membership (zero authority, fail-closed, not over-granted). Bootstrap seeds
-- a project before any real user exists, so this is not reachable in practice; the
-- fail-closed direction is the deliberate choice for the pathological ordering.
INSERT INTO org_memberships (user_id, org_id, role)
SELECT u.id, p.org_id,
       CASE
         WHEN u.role = 'admin'                              THEN 'owner'
         WHEN u.role IN ('owner','admin','member','viewer') THEN u.role
         ELSE 'viewer'
       END
FROM users u
CROSS JOIN LATERAL (SELECT org_id FROM projects ORDER BY created_at ASC LIMIT 1) p
ON CONFLICT (user_id, org_id) DO NOTHING;
