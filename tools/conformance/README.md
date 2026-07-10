# tools/conformance

The conformance test harness — the "verified" bar.

> **Storage-adapter conformance lives in the kernel module** at
> [`kernel/tools/conformance`](../../kernel/tools/conformance) — it must import
> the internal storage interface (`kernel/internal/storage`) and the in-tree
> adapters, which are not importable from this root-level, non-module directory.
> Run it with `cd kernel && go test ./tools/conformance/...`. It replays the
> normative merge V-vectors and the order-independence property against every
> adapter's `storage.MergeConformer`. See
> [docs/adapters/authoring-a-storage-adapter.md](../../docs/adapters/authoring-a-storage-adapter.md).

The remaining responsibilities below are dialect/plugin conformance:

1. **Dialect conformance (D3):** replay every fixture under
   `kernel/testdata/fixtures/**` through the ingestion pipeline and assert the
   canonical output. Backs the `conformance` CI workflow.
2. **Plugin conformance:** validate that a plugin honors the public contract —
   manifest validity, capability usage within grants, surface registration,
   graceful degraded states — so "verified" means something concrete for both
   first- and third-party plugins.

Every plugin-facing contract change should be covered here (part of the
definition of done for plugin-facing changes).
