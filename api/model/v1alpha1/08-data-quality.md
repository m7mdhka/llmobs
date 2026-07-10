# Data-Quality Signals (`v1alpha1`)

**Section type:** Normative.

Across this specification the kernel **normalizes, coerces, or truncates** input
rather than failing (a resilient-ingestion stance). Every such action MUST be
**observable** — silent lossy behavior is prohibited. This chapter defines the
one consistent mechanism used everywhere, so these are not per-feature
inventions.

## 1. The two-part signal mechanism (Normative)

Every data-quality event produces both:

1. **A per-entity marker** stored in the entity's `attributes` map under the
   reserved `llmobs.dq.*` namespace, so the affected entity is self-describing
   and the condition is visible through the Query API.
2. **A kernel counter** — a telemetry metric the kernel increments per
   occurrence, labeled with at least `project_id` and the signal-specific
   dimension (e.g. field name) — so operators can monitor data quality in
   aggregate.

Additionally, when a value is **replaced** (coerced/normalized/frozen), the
**original offered value** MUST be preserved under the reserved `llmobs.raw.*`
namespace so nothing is lost (invariant 6).

The `llmobs.*` namespace is kernel-owned; normalizers and plugins MUST NOT write
arbitrary keys under it (`02-span.md` §6).

## 2. Dimension sanitization (Normative) — LM-11

`environment` MUST be sanitized by the following algorithm, executed **once**, in
the shared `normalize` middleware stage, for **every** transport including compat
plugins (so the value is stored identically regardless of wire format):

1. Trim surrounding whitespace; lowercase.
2. Strip a leading reserved brand prefix if present (the brand constant and its
   `-`-suffixed form; the reserved prefix is defined by `packages/brand` /
   `kernel/pkg/brand`, not hardcoded here).
3. Replace any character outside `[a-z0-9._-]` with `-`.
4. Truncate to a maximum of **40** characters.
5. If the result is empty (or the input was absent), the value **coerces to
   `"default"`**.

Coercion and normalization MUST NOT be silent:

- If the sanitized value differs from the offered value (including coercion to
  `"default"`), the kernel MUST preserve the offered value under
  `llmobs.raw.environment` and increment `llmobs.dq.dimension_coerced` (labeled
  `field=environment`), and set the per-entity marker
  `llmobs.dq.dimension_coerced.environment = true`.
- Because `environment` is **frozen** (`05-update-semantics.md` §5), sanitization
  happens **before** the frozen-field comparison, so the comparison is between
  sanitized values.

> Evidence: Langfuse sanitizes `environment` (lowercase, reserved-`langfuse`-prefix
> strip, 40-char cap) but does so with a silent `.catch()`→"default" and, critically,
> only on its legacy path — its OTLP path performs **no** normalization, so the same
> value is stored differently by transport (study Ch. 05 §8; Ch. 08). LM-11 fixes both
> defects: one algorithm in the shared normalize stage for all transports, and no
> silent coercion.

## 3. Signal catalog (Normative)

| Signal (`llmobs.dq.*`) | Raised when | Per-entity marker | Preserved under `llmobs.raw.*` | Counter label |
|---|---|---|---|---|
| `truncated` | An adapter truncates an oversized `input`/`output` (`02-span.md` §7.2). | `llmobs.dq.truncated = true` | — (truncated content is lost by definition; the media path avoids this) | `field` (`input`/`output`) |
| `truncated_attributes` | An adapter truncates an oversized `attributes` value beyond the per-value size cap (`02-span.md` §6.2; default 16 KB, configurable). | `llmobs.dq.truncated_attributes = true` | — (large payloads SHOULD use media references instead) | `key` (the attribute key) |
| `frozen_field_conflict` | A later event offers a different value for a frozen field (`05-update-semantics.md` §5). | `llmobs.dq.frozen_field_conflict.<field> = <count>` | `llmobs.raw.<field>` = offered value | `field` |
| `dimension_coerced` | A dimension value was normalized/coerced (§2). | `llmobs.dq.dimension_coerced.<field> = true` | `llmobs.raw.<field>` = offered value | `field` |
| `start_time_normalized` | An update offered a `start_time` differing from the frozen original (`05` §5; a specialization of `frozen_field_conflict` for the timing anchor, LM-6). | recorded as `frozen_field_conflict.start_time` | `llmobs.raw.start_time` | `field=start_time` |
| `unmapped_kind` | A source type had no mapping and fell back to `span` (`02-span.md` §2.2). | `llmobs.dq.unmapped_kind = true` | `raw_kind` already carries the source value | `raw_kind` |
| `validation_rejected` | A score failed config validation (`04-score.md` §7). | — (the entity is rejected, not stored) | — | `entity=score`, `reason` |
| `redacted` | Built-in redaction scrubbed one or more PII/secret matches from a payload field before persist (redact stage). | `llmobs.dq.redacted = { total, <rule>: <count>, … }` | — (redacted content is replaced by a token, e.g. `[REDACTED:email]`; the original is intentionally not preserved) | `rule` (the detector name) |
| `clock_skew` | Producer `event_ts` deviated from receive time beyond the configured threshold (ADR-0022). | `llmobs.dq.clock_skew = <delta_seconds>` | — | — |
| `incomplete_trace` | A span references a parent absent from the trace (upstream tail-sampling); computed at trace synthesis. | `llmobs.dq.incomplete_trace = true` (on the synthesized trace) | — | — |

Adding a new signal is additive. A consumer encountering an unknown
`llmobs.dq.*` key MUST ignore it (forward compatibility).

## 4. Reserved key reference (Normative)

| Reserved key | Meaning |
|---|---|
| `llmobs.raw.environment` | Offered environment before sanitization/coercion. |
| `llmobs.raw.start_time` | Offered start_time that conflicted with the frozen value. |
| `llmobs.raw.kind` | Offered kind that conflicted with the frozen value. |
| `llmobs.raw.level` | Source severity/level demoted from `status` (`02-span.md` §4.1). |
| `llmobs.raw.<field>` | Generic: any offered value replaced by a frozen/coerced value. |
| `llmobs.dq.<signal>[.<dimension>]` | Per-entity data-quality marker (§3). |

> Evidence: Langfuse's resilient-ingestion choices are consistently *silent* —
> environment `.catch()`→default, async score-validation drops, once-only field
> truncation with no marker (study Ch. 05 §8; Ch. 07 §4; Ch. 04 §10). This chapter
> exists specifically to make every such action observable, which is the recurring
> divergence the study's digest flags (digest §11, "silent coercion is the
> cautionary case").
