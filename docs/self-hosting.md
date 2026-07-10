# Self-hosting notes

Interim guidance for deployment shapes the flexibility audit surfaced. These are
supported patterns today; the deeper integrations they gesture at (OIDC, Tier-3
processors) are tracked separately.

## Headless (no web shell)

The kernel serves the web shell at `/` by default. To run it as a pure backend —
your own UI calls the Query API with a server-side credential (Kenji, Story 10) —
set:

```
LLMOBS_SERVE_SHELL=false
```

With the shell off, `/` is API-only; the OpenAPI at `api/openapi/v1alpha1` and a
machine API key (below) are all a non-browser client needs. This is also the
right mode for a terminal/TUI client (Omar, Story 19).

### Machine credentials

Browser callers authenticate with a session cookie; machines use a **bearer API
key**. An admin issues a scoped key once:

```
POST /v1alpha1/api-keys        (admin session + CSRF)   → { "secret": "sk-…", "public_key": "pk-…", "scopes": [...] }
GET  /v1alpha1/api-keys                                 → { "keys": [ { public_key, scopes, created_at } ] }
DELETE /v1alpha1/api-keys/{public_key}                  → revoke
```

Scopes: `ingest`, `query`, `scores:write`, `delete`. The secret is shown once.
Use it as `Authorization: Bearer sk-…` on the Query/ingest APIs.

## Custom redaction (interim: a proxy in front)

Server-side redaction-before-persistence is a real seam (`pipeline.redactStage`)
but is not yet third-party-injectable (issue #11). Until a processor-injection
contract lands, the supported pattern for PHI/PII scrubbing (Dr. Chen, Story 4)
is a **redaction proxy in front of the OTLP endpoint**:

```
services ──OTLP──▶ [ your redaction proxy ] ──OTLP──▶ LLMObs kernel (:4318 / :4317)
```

The proxy receives OTLP, scrubs prompt/completion payloads (your in-house NER
model or rules), and forwards cleaned OTLP to the kernel. Because it sits *before*
ingestion, no unredacted payload is ever persisted, and you keep it server-side
without changing every emitting service. This is a deployment component you own;
the kernel needs no changes.

## GDPR erasure

Delete every span for a user, provably:

```
DELETE /v1alpha1/spans?user_id=<id>&from=<rfc3339>&to=<rfc3339>   (delete scope)
     → { "erased": <count>, "audit_id": "era_…" }
```

The filter is mandatory and bounded (a user plus a time window); the deletion is a
hard delete (payloads removed, not merely hidden) and records an `erasure_audit`
row (actor, filter, count, time) so the erasure is auditable.
