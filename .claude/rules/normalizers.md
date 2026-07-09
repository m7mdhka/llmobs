---
scope: ["kernel/internal/dataplane/normalize/**"]
---

# Dialect normalizers

Normalizers run in the ingestion hot path (D3, D10). They map a wire dialect
(OpenInference, OpenLLMetry, Logfire, raw OTel GenAI) onto the canonical model.

- **Pure functions only.** No I/O, no network, no DB, no clock, no global
  state. Input attributes → canonical fields. This keeps them fast and trivially
  testable.
- **One file per dialect.** Keep dialects isolated; don't share mutable state
  between them.
- **Preserve raw attributes.** Canonical fields are added *alongside* the raw
  attributes, never replacing them. Downstream consumers can always see the
  original.
- **Every mapping change needs a fixture.** Add/update a recorded fixture under
  `kernel/testdata/fixtures/<dialect>/` and assert the canonical output. The
  conformance workflow replays these in CI.
- **Fixtures are synthetic only.** Never commit real user prompt/completion
  payloads.
- New ingestion formats are either a normalizer here (hot path) or a compat
  plugin with its own endpoint (cold path) — decide deliberately.
