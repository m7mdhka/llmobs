// Package costderive holds the ONE span-level cost derivation path (06-usage-cost.md
// §3.2, §4, §7). Both the ingest enrich stage (M2, pipeline/enrich.go) and the
// re-pricing backfill (M4, storage/reprice) call DeriveSpanCost — the derivation is
// NEVER forked, so a span re-priced by the backfill computes byte-identically to one
// priced at ingest. The pure money math lives in pricing.Derive; this package is the
// thin span-payload adapter around it (resolve usage, honor R1 provided-wins and R4
// no-usage-null, stamp the re-derivable pricing_snapshot_ref).
package costderive

import (
	"context"
	"strconv"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/pricing"
)

// PriceLookup is the price-table read surface derivation needs (implemented by
// postgres.PriceStore). Declared at the consumer (go-style). Resolve returns the entry
// applying to (provider, model) at a span's time, or (nil, nil) when none exists.
type PriceLookup interface {
	Resolve(ctx context.Context, provider, model string, at time.Time) (*pricing.Entry, error)
}

// DiscountLookup returns a project's residual discount multiplier (0,1]. Split from
// PriceLookup because the enrich stage resolves it once per batch and the re-pricing
// backfill resolves it once per project across a multi-project scan.
type DiscountLookup interface {
	GetDiscount(ctx context.Context, projectID string) (float64, bool, error)
}

// DeriveSpanCost recomputes cost_details / total_cost / cost_source /
// pricing_snapshot_ref for one canonical span payload IN PLACE, from its provided
// cost/usage plus the price table, applying the project discount. It returns the price
// entry it derived from (nil when no derivation happened: provided cost won, no usage,
// no model, or no price), and an error ONLY for a price-resolve FAILURE — a transient
// signal the caller classifies (the enrich stage swallows it fail-soft → cost null; the
// re-pricing backfill stops loud so a blip never rewrites an existing cost to null).
//
// cache is keyed by the CANONICAL (provider, model) so repeated spellings within a run
// share one lookup; pass a fresh map per batch/run so a price edit is never served stale
// beyond that unit.
func DeriveSpanCost(ctx context.Context, p map[string]any, prices PriceLookup, discount float64, cache map[string]*pricing.Entry) (*pricing.Entry, error) {
	// §3.2.1 — usage_details = a normalized copy of provided_usage_details (drop
	// negative/non-integer values; synthesize `total` from input+output if absent). We
	// do NOT derive usage (no tokenizer, §7.3/R4 forbids phantom usage).
	usage := normalizeUsage(p["provided_usage_details"])
	if len(usage) > 0 {
		p["usage_details"] = intMapToAny(usage)
	}

	// §4 step 1 — PROVIDED COST WINS and SHORT-CIRCUITS derivation (R1). No price entry
	// is consulted; pricing_snapshot_ref stays null.
	if pcd, ok := p["provided_cost_details"].(map[string]any); ok && len(pcd) > 0 {
		p["cost_details"] = copyAnyMap(pcd)
		p["cost_source"] = "provided"
		if _, has := p["total_cost"]; !has {
			p["total_cost"] = sumCostMap(pcd)
		}
		return nil, nil
	}

	// §4 step 3 default (R4) — no usage → no derived cost, leave cost null (never zero).
	if len(usage) == 0 {
		return nil, nil
	}
	model, _ := p["model"].(string)
	provider, _ := p["provider"].(string)
	if model == "" {
		return nil, nil // no served model → no price lookup (§4 step 3)
	}

	key := pricing.CanonicalProvider(provider) + "\x1e" + pricing.CanonicalModel(model)
	entry, cached := cache[key]
	if !cached {
		e, err := prices.Resolve(ctx, provider, model, SpanTime(p))
		if err != nil {
			return nil, err // resolve failure — the caller classifies (fail-soft vs stop-loud)
		}
		cache[key] = e
		entry = e
	}
	if entry == nil {
		return nil, nil // no price entry → cost null (§4 step 3), never zero
	}

	// §4 step 2 + §7 — derive, stamp derived cost + the re-derivable snapshot ref.
	costDetails, total := pricing.Derive(usage, entry, discount)
	if len(costDetails) == 0 {
		return nil, nil
	}
	p["cost_details"] = floatMapToAny(costDetails)
	p["total_cost"] = total
	p["cost_source"] = "derived"
	p["pricing_snapshot_ref"] = map[string]any{
		"type":  "price",
		"id":    entry.ID,
		"label": entry.Provider + "/" + entry.Model + " v" + strconv.Itoa(entry.Version),
	}
	return entry, nil
}

// normalizeUsage returns provided usage as map[string]int64: non-negative integer
// values only, `total` synthesized from input+output when absent (§3.2.1). Handles both
// int64 (native ingest) and float64 (JSON-round-tripped) inputs.
func normalizeUsage(v any) map[string]int64 {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return nil
	}
	out := map[string]int64{}
	for k, raw := range m {
		if n, ok := toNonNegInt(raw); ok {
			out[k] = n
		}
	}
	if _, has := out["total"]; !has {
		in, iok := out["input"]
		o, ook := out["output"]
		if iok && ook {
			out["total"] = in + o
		}
	}
	return out
}

func toNonNegInt(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		if t >= 0 {
			return t, true
		}
	case int:
		if t >= 0 {
			return int64(t), true
		}
	case float64:
		if t >= 0 && t == float64(int64(t)) {
			return int64(t), true
		}
	}
	return 0, false
}

// SpanTime reads a span payload's start_time as UTC, defaulting to now when absent or
// unparseable (a span with no start still resolves to the current price).
func SpanTime(p map[string]any) time.Time {
	if s, ok := p["start_time"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

func intMapToAny(m map[string]int64) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func floatMapToAny(m map[string]float64) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyAnyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func sumCostMap(m map[string]any) float64 {
	if t, ok := toF(m["total"]); ok {
		return t
	}
	var sum float64
	for _, v := range m {
		if f, ok := toF(v); ok {
			sum += f
		}
	}
	return sum
}

func toF(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	}
	return 0, false
}
