# Opik Cross-Over Report — Round 3 (CLOSEOUT)

> Read-only against Opik; report-only. Pre-2025-07 window. Generated 2026-07-11.
> **Dispositions PENDING human ruling** (Rounds 1–2 were ruled + banked; Round 3 was
> reported and is awaiting rulings). Recorded here as the durable, citable record.

## 1. Round header

| | |
|---|---|
| **Round** | 3 of 3 (closeout) |
| **Window** | merged PRs #6908→#7059 (`merged:<2026-06-15`); closed issues #2013→#2782 (`created:<2025-07-21`) |
| **Items triaged** | ~220 (110 PRs + 110 issues) |
| **New-cross-over rate** | **~1.8% (4/220)** — collapsed, as predicted |
| **Resume cursor (if a Round 4 is run, against recommendation)** | PRs `merged:<2026-05-29` (before #6908); issues `created:<2025-05-04` (before #2013) |

**The collapse is the finding.** Opik's cost work in this window is a stream of
one-provider-at-a-time cache/cost patches (#7016 Claude/Vertex, #6980 Gemini, #6971
OpenAI Responses, #6978 Mistral, #7023 >200k tier, #7037 price overrides) — each a
special-case fix that our §7.4 general-form rule already subsumes. CH/migration/OOM
(#2464 zookeeper/local mismatch, #2379/#2220 EKS migration, #7015 filesort OOM) land
on RULING-CH9 / R-CH3 / R-CH8. MCP OAuth (#6979/#6993/#6994/#6995) is already in the
#21 harvest. Nearly every "hit" confirms an already-banked rule.

## 2. Genuinely-new cross-over findings (only 4)

| # | Opik item | What it is | Our seam | Class |
|---|---|---|---|---|
| 1 | **PR #7019** | resolve-thread-by-**project-name** path skipped the project-visibility check the by-id path enforced | Invariant 11 / permission intersection must cover name/slug lookups, not just id | 📘 design-rule / 🔵 #21 / **P0 verify** |
| 2 | **#2366** | Helm Ingress has no `secretName` for TLS | Helm-values coverage checklist addition | 📄 docs |
| 3 | **#2764** | Bitnami image/chart deprecation | airgap "vanished/relicensed upstream image" checklist (supply-side) | 📄 docs |
| 4 | **PR #6930** | defer wide-column reads past pagination | confirms keyset pagination + payload projection (DSL §7/§10) | 📘 confirm |

## 3. CONFIRMS-already-banked (dominant bucket — validation, no new action)

§7.4 (PRs #7016/#6980/#6971/#6978/#7037 — six one-provider patches = empirical proof
of the meta-lesson); §7.5 (#7023 tier); #3397 null tolerance (#2705); RULING-CH9
(#7015); R-CH3 (#2464); R-CH8 (#2379/#2220); #21 MCP OAuth (#6979/#6993/#6994/#6995);
airgap/Helm (#2764/#2048/#2366); OTLP-canonical positioning (#2054 Ruby OTel, #2566
405-on-OTLP, #2513 cross-process propagation).

## 4. Fixtures worth harvesting (small)
- #2724/#2287 — explicit `thread_id` on an OTel/SDK span must survive into session grouping.
- #2515 — concurrent `CreateSpansBatch` for the same `(project_id,id)` absorbed by idempotent merge, no error.

## 5. Bottom line & recommendation
Cross-over trend: 5.6% → 5.4% → **1.8% new**. **STOP after Round 3.** Both
independently-architected incumbents now agree on the same handful of hard surfaces,
and we have banked immunity or a pinned rule for each. The collapse is the deliverable.

## 6. Proposed dispositions (for human ruling — NOT yet banked)
- **#7019** → P0 verify (name/slug permission-intersection); file a tracking issue.
- **#2366 / #2764** → append to the Round-2 Helm-values + airgap checklists in `.claude/rules/deploy.md`.
- **#6930** → cite as keyset+projection confirmation; no code.
- **#2724/#2287, #2515** → banked fixtures.
