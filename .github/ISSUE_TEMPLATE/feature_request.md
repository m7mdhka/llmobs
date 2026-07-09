---
name: Feature request
about: Propose a capability or improvement
title: "feat: <short summary>"
labels: ["enhancement", "triage"]
---

## Problem

<!-- What problem are you trying to solve? Who has it? -->

## Proposed solution

<!-- What would you like to happen? -->

## Kernel or plugin?

Per the microkernel architecture, most features are plugins. Where does this
belong?

- [ ] A plugin (feature living on the public plugin API)
- [ ] A kernel capability (ingestion, storage, auth, registry, event bus, Query
      API, jobs/kv/secrets) — note: kernel APIs are data nouns, never feature verbs
- [ ] Unsure — help me decide

## Alternatives considered

## Additional context

<!-- Does this need an ADR? Does it affect both deployment profiles? -->
