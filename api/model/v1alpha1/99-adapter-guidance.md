# Adapter Guidance (`v1alpha1`)

**Section type:** **Informative.** Nothing in this file is normative. It records
non-binding implementation guidance for the storage adapters, derived from the
Langfuse study. Adapters MAY diverge; these are revisitable without a spec
change, because they describe physical choices that the logical model
(`00`–`08`) forbids from being observable (`00-overview.md` §1.1).

## 1. Why this is separate (Informative)

The logical model is storage-neutral. The Query API MUST behave identically over
the lite (Postgres-only) and scale (ClickHouse) adapters. Physical layout —
partitioning, sort keys, indexes, materialized views — is therefore an adapter
concern and is kept out of the normative text so that changing it never breaks a
contract.

## 2. Scale adapter (ClickHouse) — layout guidance (Informative) — LM-9

### 2.1 Sort keys

- **`project_id` leads every sort key.** Tenant isolation becomes a prefix-range
  read and per-project deletion/retention becomes index-aligned. No
  organization identifier is stored (`01-entities.md` §1).
- **Spans (tree-first ordering):** `ORDER BY (project_id, toDate(start_time),
  trace_id, id)`. Putting `trace_id` ahead of `id` clusters a trace's spans
  contiguously, optimizing the dominant "fetch the whole trace tree" read.
- **`kind` as a skip-indexed low-cardinality column, NOT in the sort key.** This
  deliberately diverges from Langfuse (below): keeping `kind` out of the primary
  sort key preserves tree-first locality; kind-scoped analytics ("all
  generations") are served by a skip index / low-cardinality scan or a
  projection (§2.3).
- **Traces:** `ORDER BY (project_id, toDate(start_time), id)`.
- **Scores:** `ORDER BY (project_id, toDate(timestamp), subject_type, id)` — with
  `subject_type` clustering scores by what they attach to.

> Evidence: Langfuse leads every fact table with `project_id` and partitions by
> month, but orders observations `(project_id, type, toDate(start_time), id)` —
> `type` (its `kind`) sits *second*, which optimizes type-scoped analytics at the
> cost of per-trace tree fetches that must fall back to the `trace_id` bloom index
> (study Ch. 03; Ch. 06 §2, §6; digest §9). This guidance keeps `project_id`-first and
> month partitioning but moves `trace_id` ahead of `kind` in the key, because the
> canonical model's dominant read is the trace tree; `kind` filtering is served by a
> skip index. This is an adapter trade-off, explicitly revisitable.

### 2.2 Partitioning and retention

- **Partition by month** (`toYYYYMM(start_time)` / `toYYYYMM(timestamp)`), so
  time-range queries prune partitions and retention drops whole partitions.
- Implement the merge fold (`05-update-semantics.md`) with
  `ReplacingMergeTree(event_ts, is_deleted)` and read-time deduplication. Reads
  MUST dedup on `(project_id, id)` (`FINAL`, or a hand-rolled
  `argMax`/`LIMIT 1 BY id, project_id`), because the logical idempotency key is
  `(project_id, id)` alone — **do not** let the physical sort key become the dedup
  identity (`01-entities.md` §3.3).

> Evidence: Langfuse's dedup identity is its full sort key, so id reuse across a day
> boundary or a name change leaves duplicate rows that only `LIMIT 1 BY id` reads
> resolve — the source of its date-boundary queue-delay hack (study Ch. 03; Ch. 07
> §6; digest §5, §6). Because this model freezes `start_time` and keys idempotency on
> `(project_id, id)`, the scale adapter avoids the hazard entirely; no date-boundary
> delay is needed (LM-6).

### 2.3 Projections and materialized views

- Kind-scoped and session-scoped analytics MAY be accelerated with **projections**
  or materialized aggregates as a **future** recovery path if skip-index scans
  prove insufficient. These are invisible to the logical model.
- Session-list acceleration (a materialized `session_id` aggregate) is an adapter
  concern (`01-entities.md` §4.1), not a logical entity.

> Evidence: Langfuse built and then *demolished* a whole AggregatingMergeTree
> pre-aggregation layer (`0023`→`0029`); the base ReplacingMergeTree won, and it
> currently ships **no** projections (study Ch. 03; digest, exec summary). Treat
> pre-aggregation as an add-later optimization, not a foundational choice.

## 3. Lite adapter (Postgres) — layout guidance (Informative)

- The merge fold MAY be implemented as an **in-place `UPDATE`** (read-modify-write
  under a row lock or an `INSERT ... ON CONFLICT DO UPDATE`) rather than
  append-only rows, as long as it reproduces the fold in `05-update-semantics.md`
  exactly — including empty-never-clobbers, deep-merge, tags-union, frozen-field
  handling, and tombstones (`is_deleted` as a boolean column).
- Open maps (`attributes`, `usage_details`, …) map naturally onto `jsonb`.
- Promoted dimensions (`environment`, `session_id`, `user_id`) SHOULD be indexed
  b-tree columns; the `attributes` `jsonb` MAY use a GIN index for containment
  queries.
- The lite profile targets the D13 performance envelope (Postgres-only,
  `docker compose up`); it need not match scale-profile analytical throughput,
  only the same observable semantics.
- **Per-field provenance & the field-path separator (adapter-internal).** The
  merge fold tracks, per field-group, the `(event_ts, event_id)` stamp that owns
  it (a `provenance` `jsonb` column) so out-of-order updates converge to the same
  state as the ordered fold. The lite adapter joins field-group path segments
  with an ASCII **SOH (`0x01`)** separator so that a dotted attribute key
  (`gen_ai.request.model`) stays one key while a genuinely nested object
  deep-merges per leaf. This separator is **adapter-internal** and never
  observable: it is legal in `jsonb` (unlike NUL, which raises `22P05`), and
  because §6.3 forbids control characters in attribute keys at normalize time,
  the separator can never collide with a real key. A different adapter MAY choose
  any encoding — the observable fold is what conformance checks.

## 4. Cross-adapter conformance (Informative)

The meta-decision behind this model: the conformance suite (`tools/conformance`)
will drive identical event sequences into both adapters and assert identical
Query-API results, using the merge test vectors (`05-update-semantics.md` §6) as
the core cases. Any place where an adapter's physical choice becomes observable is
a conformance failure, not a spec change.
