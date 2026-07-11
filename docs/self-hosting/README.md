# Self-hosting

Deployment and operations guides for running LLMObs yourself.

- **Lite profile** — Postgres-only, `docker compose up`, shared plugin runtime.
  The fast path to a running instance.
- **Scale profile** — ClickHouse + queue + S3, Helm, isolated plugin containers,
  the operator.
- **[Cost derivation](cost-derivation.md)** — how total_cost is computed (derive-once-at-ingest, provided-wins, data-driven residual+tier pricing).
- **[Scaling from lite to scale](scaling-lite-to-scale.md)** — the never-strand
  guarantee: permanent dual-read (on by default) + the optional resumable backfill.
  Your traces never disappear crossing the boundary.
- **Air-gapped installs** — offline bundle install (a first-class target).
- Upgrades, backup/restore, and the train ↔ plugin-API compatibility matrix.
