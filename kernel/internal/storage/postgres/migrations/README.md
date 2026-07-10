# Migrations (Postgres adapter)

Numbered SQL files applied in lexical order under an advisory lock (see
`migrate.go`); each filename (sans `.sql`) is the recorded version. Migrations
are embedded into the binary.

## Policy

- **Pre-`v0.1.0` (unreleased): migrations MAY assume a fresh database.** Before
  the first tag there are no deployments to preserve, so an early migration may
  add a `NOT NULL` column without a backfill, drop-and-recreate an index, or
  otherwise assume no pre-existing rows. The lite e2e recreates the database on
  every run (`compose down -v`), so this is safe.
- **From the first tag (`v0.1.0`) onward: data-preserving paths are mandatory.**
  Every migration must be safe against a populated production database — additive
  columns are nullable or defaulted, backfills are explicit and batched where
  needed, and destructive changes go through a deprecation window. A migration
  that would lose or rewrite existing data requires an ADR.

## Conventions

- One logical change per file; never edit a migration that has shipped in a tag.
- Keep DDL idempotent (`IF NOT EXISTS` / `IF EXISTS`) so a partially-applied
  environment converges.
- The `scale` profile (ClickHouse) has its own migration set; a schema change
  that affects both profiles must land in both.
