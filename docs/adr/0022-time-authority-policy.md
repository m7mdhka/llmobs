# ADR-0022: Time authority policy (producer vs receive time)

- **Status:** Accepted (implementation scheduled)
- **Date:** 2026-07-10
- **Deciders:** m7mdhka (flexibility-audit Wave 2 remediation)
- **Relates to:** `05-update-semantics.md` (`event_ts` derivation, frozen
  `start_time`), ADR-0016. Audit Story 30 (Yusuf, edge fleet).

## Context

`event_ts` — the ordering key of the merge fold — is **derived from producer span
time** (`end_time` else `start_time`); wall-clock receive time is deliberately
not used (`05-update-semantics.md §1`). This makes the fold deterministic and
re-delivery idempotent: the same span always folds to the same state regardless
of when it arrives.

The edge-fleet reality (Story 30) breaks the assumption behind "trust the
producer": offline robots with drifting, sometimes NTP-less clocks. A fast-clock
device stamps a stale update with a *higher* `event_ts`, so it **beats a correct,
newer update** — merge ordering is silently corrupted, with no detection.

F1 ships **detection** (`llmobs.dq.clock_skew`, comparing `event_ts` to receive
time at ingest). This ADR decides the **policy** knob that detection sets up.

## Decision

A per-project ingestion policy `time_authority ∈ {producer, receive, hybrid}`,
default **`producer`**.

- **`producer` (default).** Today's behavior: `event_ts` = producer span time.
  **Deterministic and idempotent** — a batch re-upload after a crashed sync folds
  identically. This stays the default precisely because most producers have good
  clocks and idempotent re-delivery is worth more than skew-robustness.
- **`receive`.** `event_ts` = ingest receive time. Robust to producer clock skew,
  but it **breaks idempotent re-upload**: the same span re-delivered after a crash
  gets a *new* (later) receive time, so it wins over its own earlier delivery and
  can clobber a legitimately newer update in between. `receive` is only safe when
  the producer never re-delivers (at-most-once transport) — a rare deployment.
  Frozen `start_time` is unaffected (it is a payload field, not the ordering key);
  only the *ordering* stamp changes.
- **`hybrid` (the recommended skew-hardening).** `event_ts` = producer time, but
  **clamped** to `[receive_time − maxPast, receive_time + maxSkew]`. A wildly
  fast/slow clock is bounded to a sane window around receive time, while a
  well-behaved producer is untouched — so idempotent re-upload still holds for
  the common case (the clamp is a pure function of producer time + receive time,
  and re-delivery of the same span at a later receive time only moves the stamp
  if it was outside the window, which is the pathological case we are bounding
  anyway). `hybrid` is the intended answer for Yusuf.

### Interaction with existing semantics
- **`event_ts` derivation:** the policy replaces the single rule in
  `05-update-semantics.md §1` with a per-project selection; `producer` is the
  status quo and remains the spec's normative default.
- **Frozen `start_time`:** unchanged — earliest-wins on the payload field,
  independent of which clock stamps `event_ts`.
- **`llmobs.dq.clock_skew`:** stamped regardless of policy (detection is always
  on); under `hybrid` it also records that a clamp occurred.

### Migration between policies
Changing a project's policy is **not retroactive** — already-stored `event_ts`
values are not rewritten (that would require re-folding history). A policy change
takes effect for events ingested after it; the provenance column records the
stamp that was in force, so a mixed-policy history folds deterministically. A
project that switches `producer → receive` should expect a one-time ordering
discontinuity at the switch boundary and is warned in docs.

## Consequences

- Fleets with hostile clocks get a bounded, honest ordering (`hybrid`) without
  losing idempotency for everyone else; `receive` exists for the rare
  at-most-once transport.
- One config knob + one clamp function in the normalize stage; provenance already
  carries per-event stamps, so mixed-policy histories are safe.
- The default stays `producer` — deterministic, idempotent, and correct for the
  overwhelming majority; skew is *detectable* out of the box (F1) and *bounded*
  on opt-in (`hybrid`).

## Alternatives considered

- **Always receive-time** — rejected: breaks idempotent re-upload for everyone to
  serve a minority with bad clocks.
- **Silently clamp always** — rejected: changes the default's determinism without
  consent; policy must be explicit per project.
