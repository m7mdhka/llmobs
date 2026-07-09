# Update Semantics, Idempotency, and the Merge Algorithm (`v1alpha1`)

**Section type:** Normative. This is the load-bearing chapter: two independent
adapters MUST implement it identically. §6 (test vectors) is executable data and
is the conformance instrument.

## 1. The event model (Normative)

Entities are not mutated in place at the logical level. An entity's observable
state is the deterministic **fold** of the ordered set of **events** that target
it. Each event is an envelope:

| Envelope field | Type | Semantics |
|---|---|---|
| `event_id` | string | Stable unique id of *this event*. Re-delivery of the same event MUST carry the same `event_id`. Used only as a deterministic tie-breaker (§2). |
| `event_ts` | timestamp | The producer-stamped logical version of this event. Re-delivery MUST carry the same `event_ts`. |
| `target` | (entity_type, project_id, id) | Which entity this event updates (`01-entities.md` §3.2). |
| `op` | `upsert` \| `delete` | §4. |
| `payload` | partial field set | For `upsert`: the fields this event sets. Absent fields set nothing. |

An adapter MAY implement this as literal event storage (event-sourced) or as an
in-place upsert that reproduces the same fold — the choice is invisible to the
Query API (`00-overview.md` §1.1).

> Evidence: Langfuse models every update and delete as an insert into a
> `ReplacingMergeTree(event_ts, is_deleted)` and reconciles at read time; its worker
> also does a read-modify-write that folds all events for an entity into one merged
> record via `overwriteObject` (study Ch. 03; Ch. 04 §6, §8; Ch. 06 §2). This model
> lifts that behavior into a storage-neutral fold so the lite (in-place UPDATE) and
> scale (ReplacingMergeTree) adapters produce identical results.

## 2. The fold (Normative)

State is computed **per field-group**. A field-group is the unit of merge:

- Each **scalar** promoted/dimension field is its own field-group
  (`name`, `status`, `end_time`, `model`, `total_cost`, …).
- Each **key** of a map field (`attributes`, `model_parameters`,
  `usage_details`, `cost_details`, `metadata`, …) is its own field-group,
  recursively for nested objects (deep merge). The unit is the leaf key path.
- `tags` is a single field-group with **union** semantics (§3).
- `is_deleted` is a field-group (§4).

For every field-group **g**, define the set `S(g)` = events whose `op = upsert`
that **set** g (i.e. provide a non-empty value for g; see §2.1). The entity's
value for g is the value from the event in `S(g)` with the greatest
**`(event_ts, event_id)`** in lexicographic order (`event_ts` first; `event_id`
breaks exact ties). If `S(g)` is empty, g takes its declared default
(`02-span.md`/`03-trace.md`/`04-score.md`) — for most fields, null/absent.

This fold is **commutative, associative, and idempotent**: applying the same
events in any order, or applying an event more than once, yields the same state.
Re-delivery is therefore a no-op, satisfying idempotency (`01-entities.md` §3.2).

### 2.1 Empty never clobbers (Normative)

An event does **not** set field-group g (does not join `S(g)`) when its payload
value for g is any of: absent, JSON `null`, the empty string `""`, or (for a map)
a key that is absent. Therefore a later event carrying a null/empty/absent value
MUST NOT overwrite a previously-set value. Clearing a value is not expressible in
`v1alpha1` (it would be a future explicit-tombstone-per-field capability).

> Evidence: Langfuse's `overwriteObject` merge rule is "empty/undefined never
> clobbers a set value; metadata deep-merges; tags union" (study Ch. 04 §6; Ch. 06
> §2; digest §5). This model adopts that rule verbatim as the normative fold.

## 3. Tags union (Normative)

`tags` (trace-level, `03-trace.md` §2) is the set union of the `tags` arrays over
all `upsert` events in `S(tags)`. Order is not significant; duplicates are not
observable. Tags are never removed by an update in `v1alpha1`.

## 4. Deletion and tombstones (Normative)

- A `delete` event sets the `is_deleted` field-group to `true` at its
  `(event_ts, event_id)`. An `upsert` event sets `is_deleted` to `false` at its
  `(event_ts, event_id)`.
- `is_deleted` follows the same greatest-`(event_ts, event_id)`-wins fold. Hence
  an `upsert` with a greater `(event_ts, event_id)` than a prior `delete`
  **resurrects** the entity, and a `delete` with a greater stamp than all upserts
  tombstones it.
- A tombstoned entity (`is_deleted = true`) MUST NOT be returned by default Query
  API reads.

## 5. Frozen fields (Normative) — LM-5, LM-6

The following fields are **frozen**: their value is fixed by the first event that
sets them and never changes.

| Entity | Frozen fields |
|---|---|
| Span | `id`, `project_id`, `trace_id`, `kind`, `start_time`, `environment` |
| Trace | `id`, `project_id`, `start_time`, `environment` |
| Score | `id`, `project_id`, `subject_type`, `subject_id`, `timestamp`, `environment` |

When a later event offers a **different** value for a frozen field, the adapter
MUST:

1. **Keep** the original (first-written) value — the frozen value is not changed.
2. **Preserve** the offered value in `attributes` under the reserved key
   `llmobs.raw.<field>` (e.g. `llmobs.raw.start_time`).
3. **Increment** the data-quality counter `llmobs.dq.frozen_field_conflict`
   (`08-data-quality.md`), tagged with the field name.
4. **NOT reject** the rest of the event — all non-frozen field-groups in the same
   event MUST still be folded normally.

`environment` frozen-conflict handling composes with dimension sanitization
(`08-data-quality.md` §2): the offered value is sanitized first, then compared.

> Evidence: In Langfuse any field in the storage sort key is effectively immutable —
> changing it splits the entity into two physical rows — and this is an unmanaged
> hazard (study Ch. 04 §6; Ch. 07 §6; digest §5, §6). LM-5/LM-6 make immutability
> explicit and *managed*: frozen fields are enumerated, a conflicting update is
> absorbed (original kept, offered preserved, counter raised) rather than silently
> creating a divergent entity. In particular a mismatched `start_time` MUST be
> normalized to the original (LM-6), never allowed to move the entity.

## 6. Normative test vectors (Normative)

The following vectors define required merge behavior. Each is: an existing entity
state **X** (the fold so far), an incoming event **Y**, and the required
resulting state **Z**. An implementation MUST reproduce **Z** exactly. These
become conformance tests (`tools/conformance`). Timestamps are abbreviated
(`t1 < t2 < t3`); `event_id` shown only where it breaks a tie.

Notation: fields not shown are unchanged/absent. `attrs` = `attributes`.

### V1 — empty never clobbers a scalar
```
X: { id:s1, kind:span, name:"plan", start_time:t1, status:{code:ok} }
Y: { op:upsert, event_ts:t2, payload:{ name:null, status:{code:null}, output:"done" } }
Z: { id:s1, kind:span, name:"plan", start_time:t1, status:{code:ok}, output:"done" }
   # name and status unchanged (null offered); output set.
```

### V2 — later non-empty scalar wins
```
X: { id:s1, name:"plan", status:{code:unset}, event_ts_of(name)=t1 }
Y: { op:upsert, event_ts:t2, payload:{ name:"replan", status:{code:ok} } }
Z: { id:s1, name:"replan", status:{code:ok} }
```

### V3 — out-of-order event loses per field-group
```
X: { id:s1, name:"replan" }   # name last set at t2
Y: { op:upsert, event_ts:t1, payload:{ name:"plan", output:"x" } }   # t1 < t2
Z: { id:s1, name:"replan", output:"x" }
   # name keeps the t2 value (greater event_ts); output had no prior value, so t1 sets it.
```

### V4 — attributes deep-merge, per-key latest wins
```
X: { id:s1, attrs:{ a:1, nested:{ p:true, q:1 } } }   # a,nested.p,nested.q set at t1
Y: { op:upsert, event_ts:t2, payload:{ attrs:{ b:2, nested:{ q:9 } } } }
Z: { id:s1, attrs:{ a:1, b:2, nested:{ p:true, q:9 } } }
   # a and nested.p untouched; b added; nested.q overwritten (t2 > t1).
```

### V5 — tags union
```
X: { id:trace1, tags:["prod","eu"] }
Y: { op:upsert, event_ts:t2, payload:{ tags:["eu","canary"] } }
Z: { id:trace1, tags:["prod","eu","canary"] }   # set union; order not significant.
```

### V6 — frozen field conflict (start_time) is absorbed, not applied
```
X: { id:s1, kind:span, start_time:t1, environment:"prod" }
Y: { op:upsert, event_ts:t2, payload:{ start_time:t5, name:"n" } }
Z: { id:s1, kind:span, start_time:t1, name:"n",
     attrs:{ "llmobs.raw.start_time": t5 },
     dq:{ frozen_field_conflict:{ start_time: 1 } } }
   # start_time keeps t1; offered t5 preserved; counter incremented; name still applied.
```

### V7 — frozen field conflict (kind) absorbed
```
X: { id:s1, kind:generation, start_time:t1 }
Y: { op:upsert, event_ts:t2, payload:{ kind:"tool_call", model:"gpt-x" } }
Z: { id:s1, kind:generation, start_time:t1, model:"gpt-x",
     attrs:{ "llmobs.raw.kind":"tool_call" },
     dq:{ frozen_field_conflict:{ kind:1 } } }
   # kind frozen at generation; model (a generation field) still applied.
```

### V8 — tombstone hides the entity
```
X: { id:s1, name:"plan", is_deleted:false }   # upsert at t1
Y: { op:delete, event_ts:t2 }
Z: { id:s1, name:"plan", is_deleted:true }    # not returned by default reads.
```

### V9 — resurrection: upsert after delete wins by event_ts
```
X: { id:s1, name:"plan", is_deleted:true }    # delete at t2
Y: { op:upsert, event_ts:t3, payload:{ output:"done" } }
Z: { id:s1, name:"plan", output:"done", is_deleted:false }
```

### V10 — stale delete loses to newer upsert
```
X: { id:s1, output:"done", is_deleted:false }  # upsert at t3
Y: { op:delete, event_ts:t2 }                  # t2 < t3
Z: { id:s1, output:"done", is_deleted:false }  # delete is older; entity stays live.
```

### V11 — idempotent re-delivery
```
X: { id:s1, name:"replan" }                    # from event E (event_id:e9, t2)
Y: { op:upsert, event_ts:t2, event_id:e9, payload:{ name:"replan" } }  # same event again
Z: { id:s1, name:"replan" }                    # unchanged; applying E twice == once.
```

### V12 — exact event_ts tie broken by event_id
```
X: { id:s1, name:"a" }                          # set by event_id:e1 at t2
Y: { op:upsert, event_ts:t2, event_id:e2, payload:{ name:"b" } }   # same event_ts, e2 > e1
Z: { id:s1, name:"b" }                          # greater event_id wins the tie.
```

### V13 — usage/cost maps merge per key (see 06-usage-cost.md)
```
X: { id:g1, kind:generation, provided_usage_details:{ input:100 } }   # at t1
Y: { op:upsert, event_ts:t2, payload:{ provided_usage_details:{ output:20, total:120 } } }
Z: { id:g1, kind:generation, provided_usage_details:{ input:100, output:20, total:120 } }
   # per-key merge; input retained, output/total added.
```

### V14 — score value fields, data_type authoritative (see 04-score.md)
```
X: { id:sc1, data_type:categorical, value_string:"good", value_numeric:null }
Y: { op:upsert, event_ts:t2, payload:{ value_numeric:1 } }
Z: { id:sc1, data_type:categorical, value_string:"good", value_numeric:1 }
   # both value fields nullable; value_numeric now the config-mapped number. No sentinel.
```

An implementation that reproduces V1–V14 for both the Postgres and ClickHouse
adapters satisfies the update-semantics conformance bar. Additional vectors MAY
be added additively.
