# #6010 — "Allow prompts to officially support full mustache syntax (conditional rendering)"

- URL: https://github.com/orgs/langfuse/discussions/6010
- Category: Ideas · Votes (2026-07-10): **37** (task estimate ~33; actual higher) · Comments: 8

**Demand (one paragraph).** Prompt-management users want **conditional sections
inside a managed prompt** — include/exclude an instruction based on a variable
(calculator-allowed tutor, subscription tier, reasoning vs non-reasoning model)
rather than maintaining two near-duplicate prompts or pushing the logic back into
application code. A user discovered the JS SDK already renders mustache
conditionals via mustache.js (unofficially); the ask is to make it **official and
work in the Langfuse UI/playground**, so the prompt in Langfuse is the *full*
picture. Sentiment is migration-blocking: multiple commenters ("won't migrate
until we get this", "would make our migration very smooth", up=18 "dearly want
this").

**Owning surface: first-party prompt-management plugin (template capability).**
Prompt management is explicitly a plugin in the LLMObs thesis, not kernel.
Conditional rendering is a **prompt-compilation feature living entirely inside
that plugin** — it uses only SDK primitives (`kv`/`write` for prompt storage,
`surface` for the editor). No kernel API, no contract change: the kernel never
knows what templating dialect a prompt plugin uses. Reasoning: this is a pure
feature-verb ("conditionals"), and per invariant D4 the kernel must not grow a
"prompts API"; it belongs to whichever prompt-management plugin owns the prompt
model. LLMObs advantage: a community author could ship a Jinja/mustache-conditional
prompt plugin variant without waiting on a monolith roadmap.
