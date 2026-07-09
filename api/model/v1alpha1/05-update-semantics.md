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

**`event_ts` for stamp-less transports (Normative).** A transport that carries a
producer version stamp (e.g. a future `langfuse-compat` dialect) MUST use that
stamp as `event_ts`. A transport that does **not** carry one — notably OTLP —
MUST derive `event_ts` **deterministically from payload content**: for OTLP a
span's `event_ts` is its `end_time` when set, else its `start_time`. Determinism
is the point: the same source event always yields the same `event_ts`, so
re-delivery folds to the same state (idempotency, `01-entities.md` §3.2). A
normalizer MUST NOT stamp `event_ts` from wall-clock receipt time.

> Evidence: Langfuse models every update and delete as an insert into a
> `ReplacingMergeTree(event_ts, is_deleted)` and reconciles at read time; its worker
> also does a read-modify-write that folds all events for an entity into one merged
> record via `overwriteObject` (study Ch. 03; Ch. 04 §6, §8; Ch. 06 §2). This model
> lifts that behavior into a storage-neutral fold so the lite (in-place UPDATE) and
> scale (ReplacingMergeTree) adapters produce identical results.

## 2. The fold (Normative)

State is computed **per field-group**. A field-group is the unit of merge:

- Each **scalar** promoted/dimension field is its own field-group
  (`name`, `status`, `end_time`, `model`, `total_cost`, …). An **object-valued**
  scalar (e.g. `status = {code, message}`) is a **single** field-group replaced
  **wholesale** — it is NOT deep-merged. So a later `status: {code: "error"}`
  replaces the whole object and drops a prior `message`; this is deliberate
  (a stale `ok`-era message on an errored span is worse than no message). Only
  the map fields (next bullet) deep-merge (V17).
- Each **key** of a map field (`attributes`, `model_parameters`,
  `usage_details`, `cost_details`, `metadata`, …) is its own field-group,
  recursively for nested objects (deep merge). The unit is the leaf key path.
- `tags` is a single field-group with **union** semantics (§3).
- `events` (span events, `02-span.md` §4.4) is a single field-group with
  **union** semantics (§3.1).
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
value for g is any of: absent, JSON `null`, the empty string `""`, an empty array
`[]`, or (for a map) a key that is absent. A **composite/object** value is
likewise empty when **every one of its leaves is unset** by this same rule,
applied recursively — e.g. `status: {code: null}` does not set the `status`
field-group, because its only leaf is null. Therefore a later event carrying a
null/empty/absent value, or a composite with no set leaves, MUST NOT overwrite a
previously-set value. Clearing a value is not expressible in `v1alpha1` (it would
be a future explicit-tombstone-per-field capability).

> Evidence: Langfuse's `overwriteObject` merge rule is "empty/undefined never
> clobbers a set value; metadata deep-merges; tags union" (study Ch. 04 §6; Ch. 06
> §2; digest §5). This model adopts that rule verbatim as the normative fold.

## 3. Tags union (Normative)

`tags` (trace-level, `03-trace.md` §2) is the set union of the `tags` arrays over
all `upsert` events in `S(tags)`. Order is not significant; duplicates are not
observable. Tags are never removed by an update in `v1alpha1`.

### 3.1 Span events union (Normative) — Q1

`events` (span events, `02-span.md` §4.4) is the set union of the `events` arrays
over all `upsert` events in `S(events)`. Two span events are the **same** (and
deduplicated) when their `name`, `timestamp`, and `attributes` are all equal; the
observable order is by `timestamp` (ties broken deterministically by `name`).
Because identical span events collapse, re-delivery of an event carrying the same
span events is idempotent (§2). Span events are never removed by an update in
`v1alpha1`.

## 4. Deletion and tombstones (Normative)

- A `delete` event sets the `is_deleted` field-group to `true` at its
  `(event_ts, event_id)`. An `upsert` event sets `is_deleted` to `false` at its
  `(event_ts, event_id)`.
- `is_deleted` follows the same greatest-`(event_ts, event_id)`-wins fold. Hence
  an `upsert` with a greater `(event_ts, event_id)` than a prior `delete`
  **resurrects** the entity, and a `delete` with a greater stamp than all upserts
  tombstones it. Stated directionally: **a tombstone loses to any later revival
  by `(event_ts, event_id)`, and a revival loses to any later tombstone** — it is
  strictly the greatest stamp that decides, never the operation kind (V8–V10).
- A tombstoned entity (`is_deleted = true`) MUST NOT be returned by default Query
  API reads.

## 5. Frozen fields (Normative) — LM-5, LM-6

The following fields are **frozen**: their value is fixed by the **first** event
that sets them and never changes. "First" means the event with the **earliest
`(event_ts, event_id)`** (lexicographic, §2) among the events that set the field —
so the frozen value is well-defined regardless of arrival order.

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

The following vectors define required merge behavior as **executable data**:
each is a JSON object `{ name, entity, note, events, expect }`. `events` is the
ordered set of ingested events to fold (each `{op, event_ts, event_id?, payload}`);
`expect` is the required observable state (with `is_deleted`). An implementation
MUST reproduce `expect` exactly. `event_ts` and `event_id` follow §2; the fold is
order-independent, so a vector's `events` MAY be applied in any order. Frozen-field
conflicts surface `expect.attributes["llmobs.raw.<field>"]` and
`expect.dq["frozen_field_conflict.<field>"]` (§5, `08-data-quality.md`). `tags` and
`events` compare as sets/dedup-ordered. The conformance suite (`tools/conformance`)
parses these blocks and runs them against every storage adapter.

### V1 — empty never clobbers a scalar (null offered leaves value; new field set)

```json
{
  "name": "V1",
  "entity": "span",
  "note": "empty never clobbers a scalar (null offered leaves value; new field set)",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "kind": "span",
        "name": "plan",
        "start_time": 1,
        "status": {
          "code": "ok"
        }
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "name": null,
        "status": {
          "code": null
        },
        "output": "done"
      }
    }
  ],
  "expect": {
    "kind": "span",
    "name": "plan",
    "start_time": 1,
    "status": {
      "code": "ok"
    },
    "output": "done",
    "is_deleted": false
  }
}
```

### V2 — later non-empty scalar wins

```json
{
  "name": "V2",
  "entity": "span",
  "note": "later non-empty scalar wins",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "name": "plan",
        "status": {
          "code": "unset"
        }
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "name": "replan",
        "status": {
          "code": "ok"
        }
      }
    }
  ],
  "expect": {
    "name": "replan",
    "status": {
      "code": "ok"
    },
    "is_deleted": false
  }
}
```

### V3 — out-of-order event loses per field-group

```json
{
  "name": "V3",
  "entity": "span",
  "note": "out-of-order event loses per field-group",
  "events": [
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "name": "replan"
      }
    },
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "name": "plan",
        "output": "x"
      }
    }
  ],
  "expect": {
    "name": "replan",
    "output": "x",
    "is_deleted": false
  }
}
```

### V4 — attributes deep-merge, per-key latest wins

```json
{
  "name": "V4",
  "entity": "span",
  "note": "attributes deep-merge, per-key latest wins",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "attributes": {
          "a": 1,
          "nested": {
            "p": true,
            "q": 1
          }
        }
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "attributes": {
          "b": 2,
          "nested": {
            "q": 9
          }
        }
      }
    }
  ],
  "expect": {
    "attributes": {
      "a": 1,
      "b": 2,
      "nested": {
        "p": true,
        "q": 9
      }
    },
    "is_deleted": false
  }
}
```

### V5 — tags union (order not significant; compared as a set)

```json
{
  "name": "V5",
  "entity": "trace",
  "note": "tags union (order not significant; compared as a set)",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "tags": [
          "prod",
          "eu"
        ]
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "tags": [
          "eu",
          "canary"
        ]
      }
    }
  ],
  "expect": {
    "tags": [
      "canary",
      "eu",
      "prod"
    ],
    "is_deleted": false
  }
}
```

### V6 — frozen field conflict (start_time) absorbed, not applied

```json
{
  "name": "V6",
  "entity": "span",
  "note": "frozen field conflict (start_time) absorbed, not applied",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "id": "s1",
        "kind": "span",
        "start_time": 1,
        "environment": "prod"
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "start_time": 5,
        "name": "n"
      }
    }
  ],
  "expect": {
    "id": "s1",
    "kind": "span",
    "start_time": 1,
    "environment": "prod",
    "name": "n",
    "attributes": {
      "llmobs.raw.start_time": 5
    },
    "dq": {
      "frozen_field_conflict.start_time": 1
    },
    "is_deleted": false
  }
}
```

### V7 — frozen field conflict (kind) absorbed; non-frozen field still applied

```json
{
  "name": "V7",
  "entity": "span",
  "note": "frozen field conflict (kind) absorbed; non-frozen field still applied",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "id": "s1",
        "kind": "generation",
        "start_time": 1
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "kind": "tool_call",
        "model": "gpt-x"
      }
    }
  ],
  "expect": {
    "id": "s1",
    "kind": "generation",
    "start_time": 1,
    "model": "gpt-x",
    "attributes": {
      "llmobs.raw.kind": "tool_call"
    },
    "dq": {
      "frozen_field_conflict.kind": 1
    },
    "is_deleted": false
  }
}
```

### V8 — tombstone hides the entity

```json
{
  "name": "V8",
  "entity": "span",
  "note": "tombstone hides the entity",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "name": "plan"
      }
    },
    {
      "op": "delete",
      "event_ts": 2
    }
  ],
  "expect": {
    "name": "plan",
    "is_deleted": true
  }
}
```

### V9 — resurrection: upsert after delete wins by event_ts

```json
{
  "name": "V9",
  "entity": "span",
  "note": "resurrection: upsert after delete wins by event_ts",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "name": "plan"
      }
    },
    {
      "op": "delete",
      "event_ts": 2
    },
    {
      "op": "upsert",
      "event_ts": 3,
      "payload": {
        "output": "done"
      }
    }
  ],
  "expect": {
    "name": "plan",
    "output": "done",
    "is_deleted": false
  }
}
```

### V10 — stale delete loses to newer upsert

```json
{
  "name": "V10",
  "entity": "span",
  "note": "stale delete loses to newer upsert",
  "events": [
    {
      "op": "upsert",
      "event_ts": 3,
      "payload": {
        "output": "done"
      }
    },
    {
      "op": "delete",
      "event_ts": 2
    }
  ],
  "expect": {
    "output": "done",
    "is_deleted": false
  }
}
```

### V11 — idempotent re-delivery (same event twice == once)

```json
{
  "name": "V11",
  "entity": "span",
  "note": "idempotent re-delivery (same event twice == once)",
  "events": [
    {
      "op": "upsert",
      "event_ts": 2,
      "event_id": "e9",
      "payload": {
        "name": "replan"
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "event_id": "e9",
      "payload": {
        "name": "replan"
      }
    }
  ],
  "expect": {
    "name": "replan",
    "is_deleted": false
  }
}
```

### V12 — exact event_ts tie broken by event_id (greater wins)

```json
{
  "name": "V12",
  "entity": "span",
  "note": "exact event_ts tie broken by event_id (greater wins)",
  "events": [
    {
      "op": "upsert",
      "event_ts": 2,
      "event_id": "e1",
      "payload": {
        "name": "a"
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "event_id": "e2",
      "payload": {
        "name": "b"
      }
    }
  ],
  "expect": {
    "name": "b",
    "is_deleted": false
  }
}
```

### V13 — usage map merges per key

```json
{
  "name": "V13",
  "entity": "span",
  "note": "usage map merges per key",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "kind": "generation",
        "provided_usage_details": {
          "input": 100
        }
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "provided_usage_details": {
          "output": 20,
          "total": 120
        }
      }
    }
  ],
  "expect": {
    "kind": "generation",
    "provided_usage_details": {
      "input": 100,
      "output": 20,
      "total": 120
    },
    "is_deleted": false
  }
}
```

### V14 — score value fields, data_type authoritative; both nullable, no sentinel

```json
{
  "name": "V14",
  "entity": "score",
  "note": "score value fields, data_type authoritative; both nullable, no sentinel",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "data_type": "categorical",
        "value_string": "good",
        "value_numeric": null
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "value_numeric": 1
      }
    }
  ],
  "expect": {
    "data_type": "categorical",
    "value_string": "good",
    "value_numeric": 1,
    "is_deleted": false
  }
}
```

### V15 — span events union with dedup by (name,timestamp,attributes), ordered by timestamp

```json
{
  "name": "V15",
  "entity": "span",
  "note": "span events union with dedup by (name,timestamp,attributes), ordered by timestamp",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "events": [
          {
            "name": "gen_ai.user.message",
            "timestamp": 1,
            "attributes": {
              "content": "hi"
            }
          }
        ]
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "events": [
          {
            "name": "gen_ai.user.message",
            "timestamp": 1,
            "attributes": {
              "content": "hi"
            }
          },
          {
            "name": "gen_ai.choice",
            "timestamp": 2,
            "attributes": {
              "finish_reason": "stop"
            }
          }
        ]
      }
    }
  ],
  "expect": {
    "events": [
      {
        "name": "gen_ai.user.message",
        "timestamp": 1,
        "attributes": {
          "content": "hi"
        }
      },
      {
        "name": "gen_ai.choice",
        "timestamp": 2,
        "attributes": {
          "finish_reason": "stop"
        }
      }
    ],
    "is_deleted": false
  }
}
```

### V16 — composite with no set leaves never clobbers (status:{code:null})

```json
{
  "name": "V16",
  "entity": "span",
  "note": "composite with no set leaves never clobbers (status:{code:null})",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "status": {
          "code": "ok",
          "message": "done"
        }
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "status": {
          "code": null
        },
        "name": "n"
      }
    }
  ],
  "expect": {
    "status": {
      "code": "ok",
      "message": "done"
    },
    "name": "n",
    "is_deleted": false
  }
}
```

### V17 — object-valued scalar (status) replaces wholesale — a later status drops a prior message

```json
{
  "name": "V17",
  "entity": "span",
  "note": "object-valued scalar (status) replaces wholesale \u2014 a later status drops a prior message",
  "events": [
    {
      "op": "upsert",
      "event_ts": 1,
      "payload": {
        "status": {
          "code": "ok",
          "message": "done"
        }
      }
    },
    {
      "op": "upsert",
      "event_ts": 2,
      "payload": {
        "status": {
          "code": "error"
        }
      }
    }
  ],
  "expect": {
    "status": {
      "code": "error"
    },
    "is_deleted": false
  }
}
```

An implementation that reproduces V1–V17 for both the Postgres and ClickHouse
adapters satisfies the update-semantics conformance bar. Additional vectors MAY
be added additively.
