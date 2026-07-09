# docs/

Documentation site source (Docusaurus or similar) plus Architecture Decision
Records.

| Path | Purpose |
|---|---|
| `adr/` | ★ Architecture Decision Records — one per decision, numbered, immutable once accepted. Read before proposing architectural changes; add one for any new architectural decision. |
| `contributing/` | Contributor-facing docs (workflow, environment). |
| `plugin-authors/` | THE tutorial: "your first plugin in Python", plus plugin API reference. |
| `self-hosting/` | Lite and scale deployment guides; upgrades. |

The docs site is built and deployed by the `docs` CI workflow on merges to
`main`. The train ↔ plugin-API compatibility matrix is a generated table under
`docs/` (per `VERSIONING.md`), tested in CI.
