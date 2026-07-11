# Pricing schemas (`v1alpha1`)

The contract for the **price table** — the artifact cost derivation reads
(ADR-0029; `api/model/v1alpha1/06-usage-cost.md` §4–§5, §7). Price entries are
control-plane reference metadata (Postgres, both profiles), not telemetry, so this
shape is not part of the canonical span model (`api/model/`) and is not fed to
`kernel/pkg/model`; the kernel's `postgres.PriceStore` row struct mirrors it by hand
(as `api_keys` does).

- `price-entry.schema.json` — one immutable, versioned pricing snapshot for a
  `(provider, model)`. Its `id` (`"<provider>/<model>#<version>"`) is also the
  `pricing_snapshot_ref.id` a derived cost stores (§5, D4), so cost is re-derivable.
- Rates are **data-driven** (§7.4/R4): a `key -> { per_token, reduces? }` map, one code
  path for all providers/buckets. `reduces` declares the residual base (§7.3/R3).
- `tiers` carries above-threshold rates (§7.5/R7); the applied schedule is the entry
  version, so re-pricing (§5/R8) is deterministic.

Additive-only at this maturity (`v1alpha1`): new optional fields / new rate keys are
additive; removing or repurposing a field requires an ADR + deprecation window.
