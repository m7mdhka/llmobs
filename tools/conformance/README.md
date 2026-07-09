# tools/conformance

The conformance test harness — the "verified" bar. Two responsibilities:

1. **Dialect conformance (D3):** replay every fixture under
   `kernel/testdata/fixtures/**` through the ingestion pipeline and assert the
   canonical output. Backs the `conformance` CI workflow.
2. **Plugin conformance:** validate that a plugin honors the public contract —
   manifest validity, capability usage within grants, surface registration,
   graceful degraded states — so "verified" means something concrete for both
   first- and third-party plugins.

Every plugin-facing contract change should be covered here (part of the
definition of done for plugin-facing changes).
