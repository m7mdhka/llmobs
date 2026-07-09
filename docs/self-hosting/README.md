# Self-hosting

Deployment and operations guides for running LLMObs yourself.

- **Lite profile** — Postgres-only, `docker compose up`, shared plugin runtime.
  The fast path to a running instance.
- **Scale profile** — ClickHouse + queue + S3, Helm, isolated plugin containers,
  the operator.
- **Air-gapped installs** — offline bundle install (a first-class target).
- Upgrades, backup/restore, and the train ↔ plugin-API compatibility matrix.
