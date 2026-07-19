# What a plugin can and can't do — and why

The honest map of the plugin boundary. Each limit below is a **deliberate design
decision**, not a missing feature — and each has a supported escape. We document our edges
this plainly on purpose: an evaluator should be able to see exactly where the walls are
and why, and decide with full information. (Neither incumbent documents theirs this
honestly.)

If you remember one thing: **the kernel owns ingestion, storage, auth, and the plugin
lifecycle; everything else is a plugin.** The walls all follow from keeping that kernel
small and its guarantees enforceable.

---

## Wall 1 — You can't run on the ingestion hot path

**The wall.** A plugin cannot insert code into the OTLP ingestion / normalization path.
Dialect normalization is kernel-only (pure functions, fixture-tested,
`kernel/internal/dataplane/normalize/`).

**Why.** The hot path decides latency and correctness for *every* span the platform
ingests. Third-party code there would make throughput and data integrity un-guaranteeable
and un-attributable. Keeping it kernel-only is what lets us promise bounded ingest and a
canonical model.

**The escape — react post-persist via `events`.** A plugin subscribes to `span.ingested`
(published from the pipeline *after* persist) and does its work off the hot path —
enrichment, scoring, alerting, export. At-least-once delivery, durable, replayable. You
see every span; you just see it a moment after it's safely stored, not in the middle of
storing it.

*Classify your failures.* Delivery is at-least-once, so your handler must be idempotent
(dedupe on the event `id`). When a handler fails, the kind of failure decides what you
do — and getting this wrong is the classic event-consumer bug:

- **Transient** (your downstream is momentarily down): do nothing — don't ack. The event
  is re-delivered on the next poll. It is retried, never dropped.
- **Permanent** (a malformed subject, a record your handler can never process): call
  `events.fail(topic, id, reason)` — or, in the `subscribe()` loop, throw
  `PermanentEventError(reason)`. The event is dead-lettered and skipped so the good
  events queued behind it keep flowing; its payload stays in the log for inspection.

Never "ack past" a permanent failure to move on — that is silent data loss. And never
retry a permanent failure forever — one poison event left un-failed eventually forces the
backlog cap to bulk-drop it *and the good events behind it*. `fail` is the one right
move. `fail`'s `id` must be the head of your unacked window (ack the good events before
the poison first); failing ahead of the head is a `409`.

**If you truly need a new wire dialect on the hot path**, that is a kernel normalizer (a
pure function + a fixture) or a cold-path compat plugin with its own ingest endpoint
(`cap:ingest`) — a deliberate choice, not an in-process hook.

---

## Wall 2 — Your frontend is semi-trusted (same-origin), not hard-confined

**The wall.** A frontend-only plugin runs in the shell's browser origin and JS realm. Its
frontend token is **least-privilege by default but not a hard boundary** — cooperating SDK
code is confined to `plugin-grant ∩ user ∩ project`, but nothing in the browser stops
plugin code from using the ambient session cookie directly.

**Why.** Same-origin Module Federation is what makes a plugin frontend fast, composable,
and able to share the design system — at the cost of a hard in-browser boundary. We do not
hide this; it's stated in [trust-model.md](trust-model.md) and proven by a passing test
(`query.TestFrontendTokenIsNotABoundary`). A frontend-only plugin is therefore
**trusted-at-install**, the same trust class as a browser extension.

**The escape — ship a backend for untrusted logic.** The double token genuinely confines a
backend: every call is re-intersected server-side, the session cookie is never forwarded,
and audience-bound assertions stop one plugin acting as another. Untrusted or
security-sensitive logic goes in a backend, full stop.

**The future.** Real confinement for *untrusted third-party* frontends — a sandboxed
cross-origin iframe + postMessage bridge, frontend token as the only credential — is a
specified follow-up (ADR-0004 amendment / ADR-0023 deferred), to be built when running
untrusted third-party frontends is a real requirement (a marketplace), not before.

---

## Wall 3 — Plugins are islands; they compose over HTTP, not in-process

**The wall.** There is no in-process plugin-to-plugin API. A plugin can't call into
another plugin's code or read another plugin's storage. The double-token protocol makes
"act as another plugin" inexpressible by construction (ADR-0023).

**Why.** Islands are what make plugins independently installable, uninstallable, and
versionable in any language without reading each other's code — the whole microkernel
thesis. A shared in-process API would re-couple release cycles and break the security
model.

**The escape — integrate as strangers over HTTP.** A provider plugin exposes its own API
from its backend; a consumer plugin calls it as an external service (`secrets` for the
credential, the egress rules for the call). Full details in
[composing-plugins.md](composing-plugins.md).

---

## Wall 4 — Large binaries don't go in kernel storage (yet)

**The wall.** There's no first-class `blobs` primitive. `kv` is small key→value; `store`
is structured relational entities. Neither is a BLOB store.

**Why.** A blob store is a real, additive primitive (a new adapter across both profiles,
signed URLs, lifecycle/GC) — an ADR-level arc of its own (tracked as #120), not something
to bolt onto the frontend arc.

**The escape — bring your own bucket.** Hold bucket creds in `secrets`, move bytes from
your backend under the egress rules, hand the frontend short-lived signed URLs, keep only
references in kernel storage. Full details in [large-artifacts.md](large-artifacts.md).

---

## What is NOT a wall (things people assume are limits but aren't)

- **Framework choice.** Your frontend can be **React, Vue, Svelte, or vanilla** — it
  exports the neutral `mount(element, context)` contract (ADR-0030). React is one binding,
  not a requirement. (This *was* the #1 wall; it's gone.)
- **Language choice (backend).** A plugin backend is an HTTP service in **any language**
  behind the double-token proxy. Always was.
- **Owning your own structured data.** The `store` primitive gives plugins tenant-scoped,
  queryable, plugin-owned collections (prompts, datasets, experiment runs) — with a
  cross-tenant isolation guarantee. You don't reach for your own DB.
- **A settings UI.** Declare `settingsSchema` for a free SchemaForm tab, or
  `settingsView: custom` to mount your own editor (rule-builder, visual editor) with opaque
  storage — secrets stay encrypted and never-returned either way.
- **Localization / RTL.** The mount context carries `locale` + `direction`; `@llmobs/ui`
  flips for RTL via logical CSS; strings externalize through `createTranslator`.

---

## The bottom line

**"Any language, any framework, plug-and-play" is now true** for the shapes real plugins
take: any-language backends, any-framework frontends, plugin-owned queryable data, a free
or custom settings UI, localization. The walls that remain — hot-path exclusion,
same-origin frontend trust, plugin islands, no first-class blobs — are **deliberate**, each
with a supported escape, and the two that are genuinely additive (origin isolation, a blobs
primitive) have named future homes. That is the difference between a *designed* boundary
and a *missing* feature.
