# Entities and Identity (`v1alpha1`)

**Section type:** Normative.

## 1. Entity inventory (Normative)

The canonical model defines exactly these entities:

| Entity | Identity | Owned by | Summary |
|---|---|---|---|
| **Trace** | `(project_id, id)` | kernel | The root of one end-to-end unit of work; an aggregation over its spans. |
| **Span** | `(project_id, id)` | kernel | One unit of work inside a trace: a `point_event`, a `timed_span`, or a `generation_shaped` operation (`span-002`). |
| **Score** | `(project_id, id)` | kernel | A named, typed **measurement** attached to a subject (`score-004`). |
| **ScoreConfig** | `(project_id, id)` | kernel | The definition constraining scores of a given name/type (`score-004` §3). |
| **MediaReference** | `(project_id, sha256)` | kernel | A content-addressed pointer to an out-of-band binary payload (`span-002` §7). |

Tenancy is a two-level hierarchy above the model: **Organization → Project**.
Organization and Project are control-plane concepts owned by the kernel's
auth/tenancy subsystem, not data-model entities; every entity above is
**project-scoped** (carries `project_id`). No entity carries an organization
identifier — organization scoping MUST be resolved before a Query API call
reaches the data model.

> Evidence: Langfuse resolves org scoping entirely in Postgres/API before any
> analytical query; `project_id` is the only tenant key the analytical store sees,
> and it leads every physical sort key (study Ch. 14 §1; Ch. 03). This model keeps
> the same "project is the data-plane tenant boundary; org lives above" split.

## 2. Hierarchy (Normative)

```
Organization            (control plane, not an entity)
└── Project             (control plane, not an entity; project_id scopes all below)
    └── Trace
        └── Span            (parent_span_id forms an in-trace tree; a span MAY be a point event)
    └── Score               (attaches to a subject: a span, a trace, a session, or a plugin type)
    └── ScoreConfig         (definition; a Score MAY reference one)
    └── MediaReference      (referenced from span input/output/attributes via a token)
```

- A **Span** belongs to exactly one Trace (`trace_id`) and MAY nest under another
  span in the same trace via `parent_span_id`. The tree is reconstructed from
  parent pointers; the model stores **no** root/depth/path field.
- A **Score** attaches to a subject via `(subject_type, subject_id)` (`score-004`
  §5). A score is not part of the span tree.

> Evidence: Langfuse observations nest via `parent_observation_id` with no stored
> depth/path, reconstructed at read time (study Ch. 06 §4). This model adopts the
> same parent-pointer tree.

## 3. Identity and idempotency (Normative) — LM-6

### 3.1 Identifiers

- **`Trace.id`** MUST be the OTel `trace_id` when the span data originates from
  OTLP, rendered as a lowercase hex string (32 hex chars for a 16-byte id).
- **`Span.id`** MUST be the OTel `span_id` when present, rendered as a lowercase
  hex string (16 hex chars for an 8-byte id). `Span.parent_span_id`, when set,
  MUST be the parent OTel `span_id` rendered the same way.
- When an id does not originate from OTel (e.g. a compat plugin or a
  client-supplied id), the normalizer MUST derive a stable id by a deterministic
  rule declared by that normalizer (for example, a namespaced hash of the source
  id). Derivation MUST be deterministic: the same source event MUST always yield
  the same canonical id, so re-delivery is idempotent.
- **`Score.id`**, **`ScoreConfig.id`**: kernel- or client-supplied stable string
  ids, unique within `(project_id, entity)`.

### 3.2 The idempotency key

The idempotency key of a Trace, Span, or Score is **`(project_id, id)`**. Two
ingested events bearing the same `(project_id, id)` for the same entity type MUST
be treated as **the same entity** — the second is an update to the first
(§`05-update-semantics.md`), never a new row observable through the Query API.

Idempotency MUST NOT depend on any field other than `(project_id, id)` — in
particular, it MUST NOT depend on `start_time` or `name`. (This is a deliberate
divergence from Langfuse; see §3.3.)

### 3.3 Divergence from Langfuse (Informative)

> Evidence: In Langfuse the *effective* dedup identity is the full storage sort
> key — e.g. `(project_id, toDate(timestamp), name, id)` for scores — so reusing an
> `id` across a UTC-day boundary or changing `name` produces a **second** physical
> row that only `LIMIT 1 BY id` reads resolve, and a 5s/15s queue delay exists
> specifically to dodge the day-boundary hazard (study Ch. 03; Ch. 07 §6; digest
> §6). LM-6 rejects this: the canonical idempotency key is `(project_id, id)` alone,
> `start_time` is frozen (§`05-update-semantics.md` §3) so it cannot move an entity
> across a partition, and there are **no** date-boundary delay mechanisms. Adapters
> MUST NOT let a physical sort key leak into idempotency; the scale adapter's
> read-time dedup MUST key on `(project_id, id)` (§`99-adapter-guidance.md`).

## 4. What is deliberately NOT an entity (Normative)

The following are intentionally absent from the canonical model. A normalizer or
plugin MUST NOT expect the kernel to store them as first-class objects.

### 4.1 Sessions, users, threads — LM-7

`session_id` and `user_id` are **promoted free-form string dimensions** on spans
and traces (`span-002` §4), not entities. There is **no** Session, User, or
Thread entity. Consequences:

- Session- and user-level views are **derived by grouping** on the dimension at
  query time; the model defines no session/user record and no per-session
  aggregate. (Acceleration such as a materialized session index is an adapter
  concern, invisible to the logical model — §`99-adapter-guidance.md`.)
- A session MAY span multiple users; the model imposes **no** one-user-per-session
  constraint.
- Multi-turn conversations are represented as multiple traces sharing a
  `session_id`, ordered by `start_time`. There is no Thread abstraction.
- UI/session flags (bookmark, public, etc.) are **not** in this model; they are
  plugin `kv` state.

> Evidence: Langfuse models sessions/users as derived-by-grouping over free-form
> promoted trace columns with no source-of-truth entity; its one Postgres session
> row is a thin lazily-created UI-flag side-table, and multi-turn = N traces sharing
> `session_id`; multi-user sessions are permitted (study Ch. 08; digest §7). This
> model adopts that stance and pushes UI flags out to plugins.

### 4.2 Datasets, dataset runs, run items — LM-8

Datasets, dataset runs, and run items are **not** kernel entities; they belong to
the **evals plugin**. A Score MAY attach to a plugin-registered subject type such
as `evals/dataset_run_item` (`score-004` §5.2), but the canonical model stores no
dataset structure. The Langfuse dataset patterns (SCD-2 versioned items,
snapshot-into-fact-row for reproducibility) are referenced as **recommended
plugin guidance** in `score-004` §5.3, not kernel specification.

> Evidence: Langfuse keeps dataset *definitions* in Postgres and *run items* in
> ClickHouse with denormalized point-in-time snapshots so results stay reproducible
> when an item is later edited (study Ch. 10; Ch. 03; digest §8). LM-8 moves this
> whole surface into a plugin and keeps only the score↔subject linkage in the
> kernel.

### 4.3 Annotations, corrections, free-text feedback — LM-3

Free-text feedback and corrected model outputs are **not scores** and are **not**
in `v1alpha1`. They are a future `annotation` concept owned by plugins. A Score
is a measurement only (`score-004` §2).

> Evidence: Langfuse overloads its score table with CORRECTION and TEXT "scores"
> that it must then exclude from every metric via `AGGREGATABLE_SCORE_TYPES` /
> `LISTABLE_SCORE_TYPES` allow-lists (study Ch. 07 §3, §8). LM-3 refuses this
> overload: measurements are scores; corrected outputs/feedback are a separate
> plugin-owned concept. See ADR-0017.
