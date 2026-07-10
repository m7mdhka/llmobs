# #12290 — "Skills management support"

- URL: https://github.com/orgs/langfuse/discussions/12290
- Category: Ideas · Votes (2026-07-10): **24** (task estimate ~13; actual higher) · Comments: 5

**Demand (one paragraph).** As users move from single LLM calls to **agentic
solutions**, they want to **manage and version "skills"** (reusable agent
capability definitions) the same way they already manage prompts. Maintainers
(@jannikmaierhoefer, @Lotte-Verheyden, both MEMBER) respond that 5–10 users are
already **storing and versioning skills as prompts** in Langfuse — i.e. they see
skills as a special case of prompt management rather than a new primitive, and are
using upvotes to gauge whether to build a dedicated surface.

**Owning surface: community plugin (or a first-party prompt-management plugin
extension) — out of scope for the kernel.** "Skills" is a feature-verb about a
specific agent-authoring workflow; per D4 the kernel must never grow a
skills/prompts API. It maps cleanly onto the SDK primitives a prompt-style plugin
already uses: `kv`/`write` for versioned skill definitions, `surface` for the
editor UI, `events` for change notifications. Reasoning: the maintainers'
own framing ("store skills as versioned prompts") confirms this needs no new data
model — it is a plugin that reuses the versioned-artifact pattern. This is exactly
the case the plugin ecosystem exists for: a community author ships a "skills
registry" tab as a container + manifest. Low absolute votes (24) → **not a
first-party priority**, but a strong showcase of "custom tab without forking."
