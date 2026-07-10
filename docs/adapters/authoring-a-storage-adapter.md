# Authoring a storage adapter

LLMObs separates the *telemetry storage adapter* from the dataplane. The kernel's
`query` and `pipeline` packages depend only on the interfaces in
`kernel/internal/storage`; a concrete adapter (the lite Postgres adapter, a future
ClickHouse or Timescale adapter) implements them and is wired in `cmd/llmobsd`.
This is the D7 seam described in `api/model/v1alpha1/99-adapter-guidance.md`.

Adapters live **in-tree** under `kernel/internal/storage/<name>` (they are a
kernel-internal concern; plugins never touch storage, invariant 3). A Timescale
adapter is a peer of `postgres`, not a plugin.

## What to implement

Implement `storage.TelemetryStore` (see `kernel/internal/storage/storage.go`):

| Method | Contract |
|---|---|
| `PersistSpan(ctx, Event) error` | Apply one span event with merge-on-write (LM-5). Reproduce the fold in `05-update-semantics.md` exactly — empty-never-clobbers, deep-merge, tags/events union, frozen-field earliest-wins, tombstones. |
| `QuerySpans(ctx, where, args, order, limit)` | Execute a compiled spans predicate; return folded span docs (JSON). |
| `QueryTraces(ctx, where, args, order, limit)` | Execute a compiled traces predicate over your synthesized trace projection (DSL §4.1); return trace docs. |
| `GetSpan(ctx, projectID, id)` | Return a folded span doc, or nil if absent/deleted. |
| `GetTraceSpans(ctx, projectID, traceID)` | Return a trace's non-deleted spans in tree-buildable order. |

> **Note on the query signature.** The methods take a compiled SQL predicate
> (`where`, `args`, `order`) produced by the DSL→SQL compiler in
> `internal/dataplane/query`. That compiler currently emits **Postgres SQL**, so a
> Postgres-compatible adapter (Timescale) reuses it directly. A dialect that is
> *not* Postgres-compatible (ClickHouse) will need compilation moved behind an
> adapter-owned compile step (ADR-0019); that relocation is tracked as
> follow-up. If you are on a Postgres-compatible engine, the interface as-is is
> enough.

Control-plane storage (users, API keys, projects, sessions) is **not** part of
this interface. It is a separate concern accessed via the `pgxpool` handle; do
not fuse it into the telemetry adapter.

## Proving conformance

Your adapter must reproduce the normative merge. Expose a
`storage.MergeConformer` (a `Name()`, a pure `Fold`, and an incremental
`MergeIncremental`) and add a test under `kernel/tools/conformance`:

```go
func TestTimescaleConformance(t *testing.T) {
    RunConformance(t, timescale.Conformer{})
}
```

Then:

```bash
cd kernel && go test ./tools/conformance/...
```

The harness runs, against your conformer:

- **The V-vectors** (`V1`–`V17`) parsed straight from
  `api/model/v1alpha1/05-update-semantics.md` — the ordered-fold cases.
- **Order-independence** — 300 shuffles asserting `MergeIncremental == Fold`
  for any arrival order (the out-of-order guarantee, issue #17).

This is the single source of conformance truth; the Postgres adapter runs through
it in CI, and so must yours. Any place your adapter's physical layout becomes
observable through the Query API is a conformance failure, not a spec change.

## What the kernel guarantees you

- Events arrive already normalized to the canonical model (the normalize stage
  ran); you receive `storage.Event` values, not wire bytes.
- The DSL is validated (target/field/ceiling/422) before a predicate reaches you;
  a well-formed-but-unsatisfiable predicate is your job to run, not reject.
- Both deployment profiles must share one observable semantics — your adapter may
  choose any physical layout, but the Query API results must match Postgres's.
