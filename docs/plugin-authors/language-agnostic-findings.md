# The language-agnostic contract — findings from a real Python backend (H7)

This is the honest report from building `plugins/langfuse-compat` — a **Python**
backend — against the plugin protocol. The point of the exercise was to **find
problems**: wherever the contract turned out to be implicitly Go-shaped, and
wherever the Langfuse→us migration is lossy. A first cross-language integration
that surfaced nothing would mean it was under-tested; these are the frictions a
real Python author hits, each caught at the cheapest possible moment.

## What was actually RUN (not just built)

- **Cross-language token interop** — `backend/interop_test.py` verifies a
  **Go-kernel-signed** identity assertion (the golden vector pinned in
  `kernel/pkg/pluginproto/golden_test.go`) using Python `cryptography`, and rejects
  wrong-audience / expired / foreign-key. **Result: the crypto interoperates.**
  This is the load-bearing proof of the language-agnostic claim.
- **Langfuse→OTLP translation** — `backend/translate_test.py` runs the batch an
  unmodified Langfuse SDK sends through `translate.py` and asserts the OTLP shape,
  id derivation, and that unmapped types are reported.

## Contract findings (the whole point)

| # | Finding | Status |
|---|---------|--------|
| **1** | **Token segments are UNPADDED base64url** (Go `RawURLEncoding`). Python's `urlsafe_b64decode` raises `Incorrect padding` — a naive Python verifier fails outright. | **Documented + helper.** The token format is now stated as unpadded base64url in `api/plugin/v1alpha1/README.md`; the Python lib ships a re-pad `_b64url_decode`. Not a Go bug (compact is fine) — it was implicitly Go-shaped. |
| **2** | **No way for a plugin to obtain the kernel public key.** The handshake is kernel→plugin, so a plugin can't verify kernel-signed assertions — a Go plugin hid this by receiving the key in-process. | **Fixed (contract change).** Added `GET /v1alpha1/plugin/kernel-key` publishing the Ed25519 key (hex + base64). Minimal single-key form; JWKS + rotation stays deferred. |
| **3** | **Cold-path ingest can't present the double token.** A Langfuse client hits the plugin *directly* (not through the kernel proxy), so there is **no user assertion** — but `ingest` required service token **+** assertion. | **Fixed (contract change).** Ingest is now **service-token-only** (`pluginauth.RequirePluginToken`): the plugin writes telemetry into its own project, no user to intersect. Project is kernel-resolved, so "can't write outside its project" is *strengthened* (not header-controllable). |
| **4** | **Service-token delivery to the plugin is unspecified.** The supervisor *mints* the token at handshake, but nothing delivers it to the plugin — again hidden in-process for a Go plugin. | **Fixed in H7c (contract change).** The kernel PUSHES the token to the plugin's own registered URL (`POST /plugin/v1/token`) at handshake completion + on refresh; delivery is part of readiness (no token → degraded, never `running`); **no plugin-pull path** (obtaining another plugin's token isn't expressible). The Python backend receives its token and the fully-live handshake is closed. |

| **5** | **The functional-watermark *budget* degrades an idle plugin.** The fully-live e2e (H7c) auto-disabled langfuse-compat with "watermark stale past budget": handshake + token delivery *succeeded*, but the plugin is idle until a Langfuse client sends traffic, so a staleness budget faulted a perfectly healthy plugin (the B3 lesson, hit live). | **Fixed + documented (H7c).** An **idle-until-triggered** plugin declares **no `watermarkBudget`** (the watermark is then informational, not a degrade signal); a budget is only for *continuously-progressing* plugins (a poller/stream). The plugin seeds its watermark to startup so it's not a misleading `0`. |

**Real contract changes:** #2, #3 (H7b) and #4, #5 (H7c). #1 is a documented
format caveat. That is the expected shape of a first cross-language integration —
not zero friction. Notably #5 was surfaced only by the *fully-live* e2e, not by
any unit test — the whole reason to run both services together.

## Migration fidelity — Langfuse wire → canonical model

An unmodified Langfuse SDK's ingestion batch, field by field. "Migration is a
config change" is only honest with this table — it names what a migrating user
**keeps** and **loses**.

| Langfuse field | → canonical | Fidelity |
|----------------|-------------|----------|
| `trace.id`, `observation.id` | derived 16/8-byte hex (sha256); original in `langfuse.trace_id`/`langfuse.observation_id` | **derived + raw** — the id does not round-trip byte-for-byte; the original is preserved as an attribute |
| `trace.name`, `observation.name` | span name | **clean** |
| `userId` → `user.id` | `user_id` | **clean** |
| `sessionId` → `session.id` | `session_id` | **clean** |
| `timestamp`/`startTime`/`endTime` | span start/end | **clean** |
| `generation.model` → `gen_ai.request.model` | `model` | **clean** |
| `generation.usage.{input,output}` → `gen_ai.usage.*` | `provided_usage_details` | **clean** (when `unit=TOKENS`) |
| `generation.usage.unit` (CHARACTERS, …) | `langfuse.raw.usage_unit` | **partial** — our usage is token-count-oriented; non-token units land in raw |
| `generation.completionStartTime` | `completion_start_time` | **clean** (we promote this field) |
| `input` / `output` | `input` / `output` | **clean** (payload) |
| `metadata.*` | `langfuse.raw.metadata.*` | **raw** — preserved as attributes, not promoted |
| `release`, `version` | `service.version` / `llmobs.version` | **clean** |
| `level` (ERROR) | span `status.code=error` | **clean**; non-error levels → unset (**partial**) |
| `statusMessage`, `promptName` | `langfuse.raw.*` | **raw** |
| `public` (visibility flag) | `langfuse.raw.public` | **doesn't map** — Langfuse-specific concept, preserved as raw only |
| **`score-create`** | — | **doesn't map through ingest** — Langfuse scores are a separate concept; they map to our `scores:write` primitive, not the OTLP/ingest path. Reported in the batch `skipped` list, not silently dropped. |

**Bottom line for a migrating user:** traces, generations, spans, model/usage,
timings, user/session, and input/output round-trip cleanly; Langfuse ids become
derived-with-original-preserved; metadata and Langfuse-specific fields land in
`langfuse.raw.*` (queryable, never lost); scores need a one-line switch to the
score-write primitive. Nothing is silently dropped — the lossy edges are named
here and reported in the ingestion response's `skipped`.
