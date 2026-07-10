# Built-in redaction

LLMObs can scrub PII/secret patterns from payload fields **before they are
persisted**, so the stored data — and therefore any export — is safe by
construction. This is the invariant that makes Élodie's training-data exports and
Chen's compliance posture defensible: no unredacted value is ever written.

## What it does

- Runs in the `redact` pipeline stage, **before persist**.
- Scans the payload fields whose values can carry prompt/response content:
  `input`, `output`, and span-event attribute **values**. It **never** touches
  object keys (keys are structure, not payload) and **never** touches promoted
  fields (`model`, `environment`, ids, …).
- Replaces each match with a class token, e.g. `[REDACTED:email]`.
- Is **observable**: every scrub is counted and stamped as
  `llmobs.dq.redacted = { total, email: 2, … }`. Silent scrubbing is how trust
  dies — you can always tell redaction happened and how much.

### Preset detectors (conservative)

`email`, `phone`, `credit_card` (Luhn-checked — a 16-digit order number is **not**
redacted), `iban`, `secret` (common API-key/token shapes: `sk-…`, `AKIA…`,
`ghp_…`, `xoxb-…`). Structured patterns run before the greedy `phone` pattern so a
phone-shaped digit run inside a card/IBAN isn't mis-matched.

### Configuration

```
LLMOBS_REDACT_PRESETS="email,secret,iban,credit_card,phone"   # CSV; "none" disables
LLMOBS_REDACT_CUSTOM='[{"name":"ticket","pattern":"JIRA-\\d+","token":"[REDACTED:ticket]"}]'
```

Custom rules are per-deployment regexes with named replacement tokens. An
uncompilable rule is skipped (a bad rule never disables the rest).

## What it does NOT guarantee

- **It is pattern-based, not semantic.** Regexes miss PII they don't recognize
  (a name in free text, an address, a novel id format). For high-assurance
  scrubbing use a NER/ML processor — the **redaction proxy** pattern
  (`self-hosting.md`) or the future processor-injection contract (issue #11).
  Built-in redaction is a strong, cheap default, not a compliance guarantee on
  its own.
- **It does not redact promoted fields or keys** — by design. Put nothing
  sensitive in a `session_id`/`user_id`; those are dimensions.
- **Redaction is one-way.** The original value is replaced by a token and is not
  preserved anywhere (unlike coercion signals, which keep `llmobs.raw.*`).

## Export is a copy we cannot recall

Redaction protects what LLMObs *stores*. Once telemetry leaves LLMObs — exported
to a data lake, a training set, an external dashboard — **that copy is beyond our
reach**: a later GDPR erasure inside LLMObs (see `self-hosting.md`) cannot reach
into HuggingFace or your lake. Redaction-before-persist means those exports start
clean; **lineage** (tracked with datasets work) is how you find and purge
downstream copies. Design your export path assuming erasure does not propagate.
