# References (`v1alpha1`)

**Section type:** Normative except where marked *Informative*.

Many model fields point at another entity or at a plugin-owned object: a score's
`config_ref`, a generation's `prompt_ref` and `pricing_snapshot_ref`, a score's
`(subject_type, subject_id)`. This chapter defines the single reference shape and
the tolerance rules that apply to all of them.

## 1. Reference shape (Normative) — LM-12

A **reference** is a `(type, id)` pair, OPTIONALLY carrying a **label snapshot**:

| Reference field | Type | Null? | Semantics |
|---|---|---|---|
| `type` | string | no | The referent's kind (e.g. `score_config`, `prompt`, `price`, or a plugin namespaced type `evals/dataset_run_item`). |
| `id` | string | no | The referent's identity within its type and project. |
| `label` | string | yes | A human-readable snapshot of the referent captured **at write time** (e.g. a prompt's name+version). |

- A reference is always scoped to the same `project_id` as the entity that
  carries it; the project is not repeated in the reference.
- There are **no foreign keys** across the entity/plugin boundary. A reference is
  a soft pointer; the referent MAY not exist (§3).

### 1.1 References are queryable (Normative) — Q6

References stored on an entity MUST be **filterable in the Query DSL by equality**
on `(ref_type, ref_id, ref_label)`. This is a **generic** mechanism: it applies to
every reference field (`prompt_ref`, `pricing_snapshot_ref`, `config_ref`, and any
future plugin-owned reference) uniformly, with no per-reference promotion.

This recovers, generically, the query power a promoted column would give — e.g.
"all generations whose `prompt_ref` has `ref_id = X`" or "… `ref_label =
greeting@v3`" — so Langfuse's filter-/group-by-prompt capability is available
without promoting prompt fields (which would make prompts a kernel concept,
violating invariant 2). Every plugin-owned reference type gets the same filtering
for free.

> Rationale: Q6 keeps prompts (and all cross-boundary referents) plugin-owned while
> restoring the queryability that motivated promoting them. The Query DSL contract
> (a separate document) MUST expose reference-equality filters; this section is the
> data-model obligation that makes references first-class *query targets* without
> making them promoted *storage columns*.

> Evidence: Langfuse makes every cross-store/cross-entity pointer a plain string
> with no FK — eval job inputs, dataset item source ids, run-item pointers, media
> links, observation→prompt — and explicitly dropped the Postgres FKs between
> now-ClickHouse entities; its prompt linkage stores the resolved `(prompt_name,
> prompt_version)` and degrades `prompt_id` to `""` if the prompt row was later
> deleted (study Ch. 02 §12; Ch. 09 §5; digest §12). LM-12 formalizes this as a
> uniform `(type, id, label?)` reference with an explicit label snapshot.

## 2. Label snapshots (Normative)

The OPTIONAL `label` captures a human-readable rendering of the referent as it was
**at the moment the reference was written**. It exists so that a consumer can
display a meaningful reference even when the referent has since changed or been
deleted. The `label` is a snapshot: it is NOT kept in sync with the referent and
MUST NOT be treated as authoritative for anything but display.

A producer SHOULD populate `label` when a human-readable identifier is available
(e.g. `prompt_ref.label = "greeting@v3"`).

## 3. Dangling-reference tolerance (Normative)

A reference MAY dangle: its referent MAY be absent at read time (never created,
or deleted after the reference was written). Consumers MUST tolerate this:

- A consumer MUST NOT error solely because a reference does not resolve.
- When a referent cannot be resolved, a consumer SHOULD fall back to the `label`
  snapshot (if present) for display, or otherwise present the raw `(type, id)`.
- The kernel MUST NOT delete or rewrite an entity merely because one of its
  references became dangling; referential integrity across the boundary is by
  **convention**, maintained by cascade/backfill jobs, not by constraints.

> Evidence: Langfuse consumers must tolerate dangling references by construction —
> integrity depends on cascade + background deletion workers, and prompt linkage
> degrades gracefully when the prompt is gone (study Ch. 02 §12; Ch. 09 §5). This
> model mandates the same tolerance and records that a resolve-or-placeholder helper
> will be provided by the plugin SDK (forward reference; the SDK is out of scope of
> this contract).

## 4. Where references are used (Informative)

| Field | Reference `type` | Notes |
|---|---|---|
| `Score.config_ref` | `score_config` | Optional link to a ScoreConfig (`04-score.md` §3). |
| `Score.subject` | via `(subject_type, subject_id)` | The subject pair is itself a reference in all but name (`04-score.md` §5); kernel types `span`/`trace`/`session`, plugin types namespaced. |
| `Span.prompt_ref` | `prompt` | Generation prompt linkage; owned by the prompt-management plugin (`02-span.md` §5.4). |
| `Span.pricing_snapshot_ref` | `price` | The price entry used to derive cost (`06-usage-cost.md` §5). |
| media reference token | — | Media is referenced by the `@@@llmobsMedia:<sha256>@@@` token, not the `(type,id)` reference shape (`02-span.md` §7). |
