# LLMObs Implementation Backlog (canonical)

**This is the single source of truth for owed implementation work.** Every entry
traces to an already-banked decision (a pinned rule, a fixture-to-add, a verify-now, a
hardening, a design-note for a future arc). Nothing here is invented — provenance is
in the **Sources** column. Organize-only: this document does not implement anything.

- **P0** — pre-launch / live-surface correctness or security on code that **exists today**.
- **P1** — arc-gating design-work: must be honored when its arc is built; not a live gap.
- **P2** — hardening / checklists / positioning-proofs.
- **P3** — deferred/banked fixtures for future dialects/parsers.

Classification: **Type** = `enforce-pinned-rule` / `add-fixture` / `verify-now` /
`hardening` / `feature` / `design-note`. **Blocking** = what it gates. **Effort** = S/M/L.

Tier counts: **P0 = 4 · P1 = 18 · P2 = 12 · P3 = 4.**

Clusters: **A** enrich/cost-derivation (#13) · **B** auth/RBAC (#21) · **C** live-surface
verify · **D** ingest/WAL/event-bus durability · **E** outbound-fetch surface · **F**
deploy/airgap/Helm · **G** self-hosting rules (R7) · **H** banked future-dialect fixtures.

---

## ▶ START HERE — P0, ready to implement now (no arc dependency)

These four apply to code that ships today. Recommended order:

1. **C1 — Verify the Query API permission intersection covers name/slug resolution, not
   just id.** Opik #7019 shipped a resolve-by-name path that skipped the tenant-scope
   check the by-id path enforced. Audit every Query API lookup key (id **and**
   name/slug/external-ref) funnels through the one `auth()` intersection seam
   (invariant 11 / ADR-0025 R6). `verify-now`, **S**, security. Issue: **#70**.
   *(Round-3 disposition still pending — treat as provisional until ruled.)*
2. **C3 — Reject-at-validate-time any `fields.json` field a path can't evaluate.** Opik
   #6344: a filter field accepted by the API was silently ignored by the evaluator.
   Add a conformance assertion: for every field × every query path, an accepted field
   is evaluated or the query is refused (`08-data-quality.md` no-silent-loss).
   `add-fixture`, **M**. Issue: **#71**. Relates #5.
3. **A8 — Null usage-detail tolerance fixture (live normalizer, not enrich).** Opik
   #3397: `prompt_tokens_details=None` crashed the usage builder. Assert our
   `normalize/semconv.go` treats a null detail object as absent buckets, never errors
   (DSL §9.1). `add-fixture`, **S**. Issue: **#71**.
4. **D1 — Compose creds fail-loud.** `deploy/compose/{lite,scale}.yaml` boot with known
   default passwords. Switch to `${VAR:?err}` **together with** the committed dev
   env-file + `--env-file` wiring so `make dev` / kind e2e stay green. `hardening`,
   **M**. Issue: **#69** (already filed).

> Not in START HERE but P0-adjacent: **C2** (UTC-connection defense-in-depth) is a
> *completed verify* — we are immune to the corrupting #7205 shift by column type; the
> remaining force-UTC + cross-TZ conformance is P2 (Issue **#71**).

---

## P0 — pre-launch / live-surface (4)

| ID | Entry | Type | Blocking | Effort | Sources | Issue |
|---|---|---|---|---|---|---|
| C1 | Permission intersection covers name/slug resolution, not just id | verify-now | live tenant-isolation | S | round-03 §2 (#7019); ADR-0025 R6; CLAUDE.md inv-11 | #70 |
| C3 | `fields.json` field rejected-at-validate if a path can't evaluate it | add-fixture | live DSL correctness | M | round-01 §9 (#6344); DSL §9.1; `08-data-quality.md` | #71 |
| A8 | Null usage-detail objects normalize as absent, never error | add-fixture | live ingest robustness | S | round-02 §4 (#3397); issue-13 §3a; DSL §9.1 | #71 |
| D1 | Compose creds fail-loud (`${VAR:?err}` + e2e env-file) | hardening | prod copy-paste safety | M | round-01 §10 (#5596); deploy.md | #69 |

---

## P1 — arc-gating design-work (18)

### Cluster A — Enrich / cost-derivation stage (gates on the enrich stage; `pipeline/stages.go` is a no-op today → issue #13)

| ID | Entry | Type | Effort | Sources |
|---|---|---|---|---|
| A1 | Aggregate spans MUST NOT double-count leaf usage (+ dual-incumbent fixture #4695) | enforce-pinned-rule + add-fixture | M | `06` §7.1; ADR-0025 R1(a); issue-13 §1/§2; round-01 CONFIRMS |
| A2 | Synthesized `total` = input+output only; cache/reasoning never re-summed | enforce-pinned-rule | S | `06` §7.2; ADR-0025 R1(b) |
| A3 | `model` without usage MUST NOT fabricate cost (+ fixture #4508) | enforce-pinned-rule + add-fixture | S | `06` §7.3; ADR-0025 R1(c); issue-13 §2 |
| A4 | Every detail key priced at its own rate off the price entry; data-driven, no allow-list (+ cache/audio fixtures) | enforce-pinned-rule + add-fixture | M | `06` §7.4; issue-13 §1/§3a; round-01 (#5618/#6976), round-02 (#7137) |
| A5 | Tiered/threshold pricing applied; snapshot captures tier schedule (+ fixture #6982) | enforce-pinned-rule + add-fixture | M | `06` §7.5; issue-13 §2 |
| A6 | Symmetric model-key normalization + single provider-canon table (+ fixtures #5621/#6928) | enforce-pinned-rule + add-fixture | M | `06` §7.6; issue-13 §2 |
| A7 | `pricing_snapshot_ref` recorded on every derived cost; re-price backfill via jobs primitive | feature | L | `06` §5; issue-13 §1 |

*Dependency: A1–A7 all depend on the enrich stage existing. The complete ordered
rule+fixture set for that arc is in [§ Enrich-stage build spec](#enrich-stage-build-spec).*

### Cluster B — Auth / RBAC / #21 (gates on the auth arc → issue #21, demand #41)

| ID | Entry | Type | Effort | Sources | Issue |
|---|---|---|---|---|---|
| B1 | Real `resource:verb` RBAC + static role map (`perm.RoleScopes` binary→real; `query/server.go` fixed-admin → true intersection) | feature | L | issue-21 notes §6; round-01 auth | #21/#41 |
| B2 | OIDC login minting the standard session | feature | L | issue-21 notes §6 | #21 |
| B3 | SSO/domain data model (client secret separate, never-exported; per-domain enforcement as a record) | feature | M | issue-21 notes §6 (#14713) | #21 |
| B4 | SCIM under the one-shared-guard rule (authz at handler entry before any I/O) | feature | M | issue-21 notes (#14448) | #21 |
| B5 | Server-resolved subject == client-supplied identity (audit integrity) | enforce-pinned-rule | S | issue-21 notes (#14790) | #21 |
| ~~B6~~ | ~~**Plugin-token revocation seam** (per-plugin/`jti` denylist in verify path) — LIVE CODE gap, gates pilot→prod~~ **RESOLVED — Arc O / O4, PR #128 (`6b0b631`).** Revocation epoch store checked inside `plugintoken.Signer.Verify*` (the one chokepoint); per-user/plugin/`jti` denial with `revoked_at >= issued_at` (still-within-TTL denied); `RevokeUser` cascade; one supervisor disable seam. ADR-0033. | feature | M | ADR-0025 R3; ADR-0033; **#63** | #63 ✅ |
| B8 | Agent-callable identity tools privileged + scope-gated; H3 intersection on agent tokens | enforce-pinned-rule | S | ADR-0025 R5 | #21 |
| B9 | MCP OAuth dynamic client registration (RFC 7591) for agent/plugin surfaces | feature | M | round-02 §5 (#7093); issue-21 §7 | #21 |
| B10 | Group-mappable workspace + per-user project/dataset isolation | feature | M | round-02 §5 (#3327); issue-21 §7 | #21 |

### Cluster C — Live-surface verify (the non-P0 remainder)

| ID | Entry | Type | Effort | Sources | Issue |
|---|---|---|---|---|---|
| C4 | Partial-object update can't drop an unspecified sibling field (V17 vector — "prove it") | add-fixture | S | issue-13 §4 (#6761); `05-update-semantics.md` §3 | #71 |

### Cluster D — Ingest / WAL / event-bus durability (scale arc → issues #14/#16)

| ID | Entry | Type | Effort | Sources | Issue |
|---|---|---|---|---|---|
| D5 | Real S3/MinIO `ArchiveSink` (SSE + private ACL) + cold restore-and-replay that re-drives erasure tombstones before replay (G3-across-cold-restore) | feature | L | ADR-0027 Deferred; RULING-SP7 | #72 |
| D6 | WAL at-rest record encryption (secretbox master key) + authenticated (MAC'd) checkpoint | hardening | M | ADR-0027 Security/Deferred | #72 |

### Cluster E — Outbound-fetch surface (gates on kernel evals/webhooks/alerts → demand #36/#39)

| ID | Entry | Type | Effort | Sources | Issue |
|---|---|---|---|---|---|
| E1 | Outbound fetch: fail-closed at the ONE scheduling seam, hard timeout + propagated abort, enumerate-not-allowlist, SSRF block (link-local/metadata), shared-lite-runtime watchdog | design-note→enforce | L | ADR-0025 R2; plugin-author security docs | #73 |

---

## P2 — hardening / checklists / positioning-proofs (12)

| ID | Entry | Type | Effort | Sources | Issue |
|---|---|---|---|---|---|
| C2 | Force UTC on pg + CH connections; cross-TZ conformance assertion (non-UTC host ≡ UTC host) | hardening | S | round-02 §7 (#7205) | #71 |
| F1 | Airgap: bundle every image by digest; survive a vanished/relicensed upstream image | hardening | M | deploy.md; round-02 (#3172/#3305), round-03 (#2764 Bitnami) | #74 |
| F2 | Helm-values coverage: sub-path ingress, custom/duplicate labels, ExternalSecrets store name, **TLS `secretName`** | hardening | M | deploy.md; round-02 (#3291/#3783/#3089/#4033), round-03 (#2366) | #74 |
| F3 | Image hygiene: no unused interpreters/toolchains in `kernel.Dockerfile` | hardening | S | deploy.md; round-02 (#7107) | #74 |
| D7 | WAL group-commit fsync batching (throughput; correctness unaffected) | hardening | M | ADR-0027 Deferred | #72 |
| D8 | TTL reap of records stuck on a permanent non-decode failure | hardening | S | ADR-0027 Deferred | #72 |
| D9 | Redis Streams `MAXLEN` trim to ~backlogCap + DLQ policy | hardening | S | ADR-0028 Deferred | #72 |
| R4 | Any future distributed limiter: availability→fail-open, resource-protection→fail-closed (keep the distinction) | design-note | S | ADR-0025 R4 | — |
| B7 | SDK plugin-token refresh-at-ratio (0.8 of TTL) + notify-on-rotation | hardening | S | issue-21 notes §4 | #21 |
| POS1 | Positioning proof: trace-as-derived makes the span/trace publish race structurally impossible | positioning-proof | S | positioning.md; round-02 (#2782) | — |
| POS2 | Positioning proof: 503-backpressure convergence (G2) | positioning-proof | S | positioning.md; round-02 (#7091) | — |
| G2 | Session/thread grouping fixtures (explicit `thread_id` survives; large-output threads) | add-fixture | S | round-02 (#3441), round-03 (#2724/#2287) | #71 |

---

## P3 — deferred/banked fixtures for future dialects/parsers (4)

| ID | Entry | Type | Effort | Sources | Issue |
|---|---|---|---|---|---|
| H1 | OpenInference normalizer + banked fixtures (cost/usage precedence, tool-call shapes) | add-fixture→normalizer | L | ADR-0025 Deferred; `kernel/testdata/fixtures/deferred/`; demand #42 | #42 |
| H2 | OpenLLMetry + Langfuse-OTel dialect normalizers (validation worksheets exist) | normalizer | L | `api/model/v1alpha1/validation/`; #42 | #42 |
| H3 | Flue framework span-tree fixtures | add-fixture | M | ADR-0025 Deferred | #42 |
| H4 | R7 self-hosting rules — audit each on introduction: no secure-context browser crypto (#14498), subpath-aware redirects (#14397, ties B2), precision-preserving payload parse (#14449), typed-4xx for oversized responses (#14398) | design-note | S | ADR-0025 R7 | — |

---

<a name="enrich-stage-build-spec"></a>
## Per-cluster build spec — the enrich / cost-derivation stage (issue #13)

When the enrich arc is built, honor this complete ruled set **in dependency order**.
Everything below is already normative in `06-usage-cost.md` §7 + ADR-0025 R1; the
fixtures are enumerated in `issue-13-cost-derivation-design-notes.md`.

**Order:**
1. **Resolution scaffold** — resolve `(provider, model)` to a price entry; write
   `cost_source` + `pricing_snapshot_ref` (A7). No pricing math yet.
2. **Model-key + provider canonicalization (A6)** — symmetric load/lookup normalization;
   one provider-canon table. *Fixtures:* `openai/gpt-4o`, `openrouter/openai/…`,
   `anthropic/claude-3-5-…`; `vertex_ai`/`gemini`/`google_ai`, `azure`/`openai`.
3. **Base + residual pricing (A2, A4)** — `total` from input+output only; each detail
   key (cache/audio/reasoning/future) priced off its own rate; base rate bills the
   residual `input − Σ(specially-priced buckets)`. *Fixtures:* cache-read (#5618/#6976),
   audio 16× (#7137).
4. **Tiered pricing (A5)** — above-threshold tokens at tier rate; snapshot records the
   tier schedule version. *Fixture:* >200k gemini-2.5-pro (#6982).
5. **Guards (A1, A3)** — aggregate spans excluded from trace-level roll-ups (no
   double-count); no cost when a model is set but usage is absent. *Fixtures:*
   LangGraph+Pydantic-AI dual-incumbent (#4695); DSPy cache-hit (#4508).
6. **Re-price backfill (A7)** — a jobs-primitive replay against a snapshot version, not
   inline mutation.

**Meta-rule (governs all of the above):** price detail keys by **iterating the price
entry's rate fields**, never a hand-maintained case list — the recurring incumbent bug
(Langfuse + Opik alike) is an un-enumerated provider/bucket falling through to the flat
rate (`06` §7.4 meta-lesson; empirically confirmed by Opik's one-provider-at-a-time
patch history, round-03 §3).

---

## Per-cluster build spec — the auth / RBAC arc (issue #21)

Build order from `issue-21-auth-rbac-design-notes.md` §6, with the harvest additions:
1. **`resource:verb` scope model + static role map (B1)** — turns `perm.RoleScopes`
   from 2 scopes into real RBAC; makes `query/server.go`'s fixed-admin grant a true
   intersection.
2. **OIDC login (B2)** minting the standard session (subpath-aware redirects — H4/F2).
3. **SSO/domain data model (B3)** — client secret separate/never-exported; per-domain
   enforcement as a record, not an env var.
4. **SCIM under the one-shared-guard rule (B4)** — authz at handler entry before any I/O.
5. **Lifecycle (B5, ~~B6~~, B7)** — server-resolved subject == client identity; the
   **revocation seam (~~#63~~ ✅ RESOLVED, Arc O / O4, PR #128)**; refresh-at-ratio.
6. **Agent/MCP surfaces (B8, B9, B10)** — privileged scope-gated identity tools; RFC-7591
   dynamic client registration; group-mappable workspace + per-user isolation.

All ungated by plan — the wedge (three incumbent plan-gating data points: Langfuse
`admin-api`, Opik enterprise-auth + Cost-Intelligence entitlement).

---

## Plugin-flexibility dispositions (Arc N / N4 — ruled)

The plugin-viability audit's flexibility cluster, each given a ruled call (implement,
document-as-designed, or re-home) so "blocked" reads as "designed" or "deferred with a
home":

- **Framework lock-in → DONE.** The frontend contract is now framework-neutral (React is
  one binding); any Vue/Svelte/vanilla plugin ships via `mount(element, context)`
  (ADR-0030, N1). No longer a wall.
- **Cross-plugin composition → WORKING-AS-DESIGNED, documented.** The plugin-island model
  is intentional; the double-token protocol makes cross-plugin tokens inexpressible by
  construction (ADR-0023). Plugins compose over HTTP (a plugin exposes its own API; others
  integrate as strangers via `secrets` + declared egress). Documented in
  `docs/plugin-authors/composing-plugins.md`. A manifest-level plugin-to-plugin grant is a
  contract-level ADR IF a real scenario appears — none does today.
- **Blobs / large artifacts → NOT this arc; re-filed as its own design arc.** A first-class
  `blobs` primitive is ADR-level (new storage seam + adapter across both profiles). The
  interim (bring-your-own-bucket via `secrets` + declared egress, signed URLs from the
  backend) is documented in `docs/plugin-authors/large-artifacts.md`. Re-filed as its own
  arc (#120). The adapter design
  rules are already banked (#101 S3/GCS/Azure abstraction, #92 filesystem-safe keys); the
  arc starts from those.
- **Per-user settings/state → deferred to the auth arc (#21 / B10).** Per-user scope needs
  the server-resolved subject + per-user isolation the auth/RBAC arc builds; folding it
  there keeps one identity model rather than a parallel per-user scope in the settings
  store. Noted in `docs/plugin-authors/settings.md`.

**Close-out verdict (Arc N complete — Bucket-C flexibility re-run).** Is "any language,
any framework, plug-and-play" now true? **Yes, for the shapes real plugins take:**
any-language backends (always), **any-framework frontends** (N1 — the #1 wall flipped from
closed to open, provable by a working non-React plugin), plugin-owned queryable data
(`store`), a free **or custom** settings UI (N2), and **localization + RTL** (N3). The
remaining limits are documented as *designed* walls, each with a supported escape, in
`docs/plugin-authors/what-you-can-and-cant-do.md` (N5). The two genuinely-additive gaps
have named homes: **origin isolation** (untrusted-frontend hard confinement — ADR-0004
amendment, trigger = first untrusted third-party frontend) and a **`blobs` primitive**
(#120). Neither blocks the promise; both are honest deferrals, not silent gaps.

## Gaps found during consolidation (need a human ruling — NOT resolved here)

1. **Round-3 Opik dispositions are unruled.** Rounds 1–2 were ruled and banked; the
   Round-3 report (`round-03-findings.md`) is report-only. Backlog entries sourced from
   Round 3 — **C1 (#7019 P0 verify)**, F1/F2 additions (#2764 Bitnami, #2366 TLS
   secretName) — are marked provisional. C1 is filed as a tracking issue regardless
   (it is a *verify*, prudent to track), but its priority should be confirmed.
2. **Pipeline stage status vs. issues.** Ground-truth says the `redact` stage is
   **built** (real), while `sample` (#12) and `enrich` (#13) are no-ops. If redaction
   is done, issue **#11** may be closeable — needs a maintainer confirmation, not
   assumed here.
3. **`packages/schema-form` renderer is designed-not-built** (wave3 B9) and the
   **frontend-direct plugin identity / origin isolation** boundary is deferred
   (ADR-0004 amendment / ADR-0023). These gate several plugin UX stories (wave3
   B2/B4/B9) but have no dedicated tracking issue — should they get one, or ride #25?
4. **`docs/research/langfuse-study/` teardown book is uncommitted** (ADR-0026 Deferred,
   "lives in conversation history"). A provenance gap, not an implementation item —
   flag for reconstruct-and-commit or explicit drop.
5. **Demand issues #35–#42** (n8n, webhooks, full-text search, multimodal, alerts,
   deploy stacks, RBAC, LangChain/LlamaIndex normalizers) are product features tracked
   separately; they are cross-referenced here (E1↔#36/#39, B1↔#41, H1/H2↔#42) but their
   prioritization vs. this correctness/hardening backlog is a roadmap call for a human.

---

## Issue index

- **#13** enrich/cost-derivation (Cluster A) · **#21** auth/RBAC (Cluster B) ·
  ~~**#63** plugin-token revocation (B6)~~ ✅ (O4, PR #128) · **#69** compose hardening (D1) ·
  **#14/#16** scale bus/adapter (Cluster D) · **#12** sampling stage · **#5** SDK
  prevalidation (relates C3) · **#25** Tier-3 plugins · **#35–#42** demand.
- **New (filed by this consolidation):** **#70** name/slug tenant-scope verify (P0) ·
  **#71** live-surface conformance verifies (C2/C3/C4/A8/G2, P1) · **#72** WAL/bus scale
  hardening (D5/D6/D7/D8/D9, P1) · **#73** outbound-fetch design-rule (E1, P1) ·
  **#74** deploy hardening checklist (F1/F2/F3, P2).

*(New issue numbers are filled in by the reconciliation step; if a number here is a
placeholder, see `gh issue list --label backlog`.)*
