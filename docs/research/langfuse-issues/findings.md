# Langfuse Issues — Wave-1 Demand & "Not Planned" Analysis

Mined **2026-07-10** via the GitHub Search API against `langfuse/langfuse`
(`is:issue is:closed sort:reactions-+1-desc`). Votes and `state_reason` are
point-in-time (2026-07-10); the raw ranked set is in
[`dataset.json`](dataset.json). This is the committed source for the
"~40% Not Planned" figure used in [`../../positioning.md`](../../positioning.md)
Pillar 4 — it is no longer an uncited number.

## The headline: 40% of top-voted closed issues are "Not Planned"

Of the **25 most-upvoted closed issues**, **10 are `state_reason: not_planned`**
= **40%** (the same ratio holds at top-30: 12/30). These are not stale or
low-signal asks — they are among the single most-upvoted issues in the tracker,
and the incumbent declined them:

| # | 👍 | Title (truncated) |
|---|---:|-------------------|
| #6906 | 19 | conditional rendering in prompt management |
| #11109 | 19 | Experiments/Evaluators fail to parse Anthropic thinking blocks |
| #9473 | 18 | Langchain JS: set spans active in OTEL context |
| #2169 | 13 | add `py.typed` to python sdk (mypy typecheck) |
| #5486 | 11 | comments on dataset items |
| #5142 | 11 | setting to auto-render all images |
| #1911 | 7 | log for function-calling in Langchain |
| #2785 | 7 | add Gemini-1.5 input/output prices to the model table |
| #3380 | 7 | add `fetch_score(s)` in the SDKs |
| #9257 | 7 | support `gen_ai.system_instructions` (OTel gen-ai semconv) |

**Reading (no FUD, structural):** this is monolith economics, not laziness.
Every request — however niche — lands on one core team's single roadmap, and
most must lose. Several of these are exactly the kind of feature that, in a
kernel-plus-plugins model, is a *community plugin* and never queues behind a core
team: conditional prompt rendering is a prompt-plugin feature; auto-render-images
is a tracing-plugin setting; per-cloud pricing rows are a cost-plugin table.
That is the launch-narrative argument for extensibility, written in the
incumbent's own tracker.

## Secondary finding: half the page is the SDK-ownership treadmill

The top closed issues cluster heavily around **owning instrumentation SDKs**:
Python 3.14 incompatibility (#9618, the single most-upvoted at 49👍), and a
recurring async/streaming context-propagation failure class — #3961 (FastAPI
StreamingResponse splits traces), #8780 ("Failed to detach context"), #8216
(`@observe` + FastAPI streaming), #9473 (LangChain JS OTEL context), plus
#5704 (Jest dynamic imports), #2169 (`py.typed`), #9770 (LangChain v1). Roughly
half the page is the maintenance cost of shipping and versioning SDKs across a
churning framework ecosystem.

**Our residue is small and known.** Our no-SDK / OTLP-canonical decision
outsources this maintenance to the much larger OTel ecosystem. What survives for
us is a fixture obligation, not an SDK: streaming/async is where upstream
instrumentation breaks, so **broken-parentage and split-trace inputs must be in
our normalizer fixture set** — malformed reality arriving at ingestion, handled
by orphan-promotion + `incomplete_trace` (already built). #9257
(`gen_ai.system_instructions`, declined) also validates a structural advantage:
because we preserve raw attributes alongside canonical fields, a lagging
normalizer loses nothing — it only delays promotion, never data.

## Absorption backlog (rules taken from declined/painful issues)

- **#517** (deploy with a given API token): IaC-deterministic bootstrap — allow
  supplying key material via config, not print-once. Deployment backlog.
- **#7122 / Vertex auth**: evals-plugin judge connections need cloud-native auth
  (assume-role, ADC, workload identity) from v1; static keys are a repeated wound.
- **#5057** (couldn't delete an evaluator): every evals-plugin entity ships with
  full CRUD including delete, day one.
- **#11109** (thinking-block parsing breaks evaluators): judge input-mapping must
  never assume message shapes; reasoning blocks / tool calls / future shapes must
  degrade gracefully — our opaque-payload + content-type design already leans right.
- **#2785 / #1874 / #2734**: repeated price-table pain → user-editable, versioned
  price tables (issue #13) is beyond debate.

> Scope honesty: this is a **Wave-1 partial** — the top-40 closed issues by
> reactions, not the full issue corpus. The 40% figure is exact **for that
> defined set** (top-25 closed by votes) and reproducible from `dataset.json`
> and the query string recorded there. It is not a claim about all issues.
