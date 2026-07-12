-- Arc O / O6 (#69) — per-user scope for the plugin kv store.
-- kv rows gain a user_id dimension: '' = PROJECT scope (shared across the project's users,
-- the existing behavior), a non-empty user identity = USER scope (per-user, isolated). The
-- user identity is the O1-resolved acting user (the verified assertion Sub), never a client-
-- supplied field — so one user's per-user state is unreadable/unwritable by another, even in
-- the same project. Existing rows default to '' (project scope) — a no-op for current data.
ALTER TABLE plugin_kv ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE plugin_kv DROP CONSTRAINT IF EXISTS plugin_kv_pkey;
ALTER TABLE plugin_kv ADD PRIMARY KEY (plugin_id, project_id, user_id, key);
