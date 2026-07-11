package pipeline

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/pricing"
)

// PriceResolver is the price-table surface the enrich stage needs (implemented by
// postgres.PriceStore). Declared at the consumer (go-style). Resolve returns the entry
// applying to (provider, model) at a span's time, or (nil, nil) when none exists.
type PriceResolver interface {
	Resolve(ctx context.Context, provider, model string, at time.Time) (*pricing.Entry, error)
	GetDiscount(ctx context.Context, projectID string) (float64, bool, error)
}

// enrich resolves usage_details and derives cost_details/total_cost/cost_source/
// pricing_snapshot_ref for each span (06-usage-cost.md §3.2, §4, §7), DERIVE-ONCE at
// ingest (not read time). It is fail-soft: a price-lookup error leaves cost null and
// never fails the pipeline (§9.1 — bad data / a transient lookup never breaks a valid
// ingest; a later re-pricing backfill fills it in). No price table configured (nil
// resolver) → a no-op pass-through, so cost is simply absent, never wrong.
type enrichStage struct {
	prices PriceResolver
	log    *slog.Logger
}

func (s *enrichStage) Name() string { return "enrich" }

func (s *enrichStage) Process(ctx context.Context, ing *Ingestion) error {
	if s.prices == nil || len(ing.Events) == 0 {
		return nil
	}
	// Per-batch caches: the discount is one lookup per project (all spans share it), and
	// (provider,model) entries repeat within a batch. Fresh each Process call → never
	// serves a price edit stale beyond one ingest batch.
	discount, _, derr := s.prices.GetDiscount(ctx, ing.Identity.ProjectID)
	if derr != nil {
		// A discount-lookup error must NOT derive at full price — that would silently
		// OVERCHARGE a discounted project and stamp cost_source=derived, which a
		// null-based re-pricing backfill (M4) cannot detect (the invariant #12 trap: the
		// failure path producing wrong-but-plausible data). Instead skip derivation for
		// this batch, leaving cost NULL — the backfill re-prices it correctly later. This
		// mirrors the price-resolve failure (also null, backfillable).
		if s.log != nil {
			s.log.Warn("enrich: discount lookup failed; leaving cost null (backfill will re-price)", "err", derr.Error())
		}
		return nil
	}
	entryCache := map[string]*pricing.Entry{}
	for _, ev := range ing.Events {
		s.enrichOne(ctx, ev.Payload, discount, entryCache)
	}
	return nil
}

// enrichOne mutates one canonical span payload in place.
func (s *enrichStage) enrichOne(ctx context.Context, p map[string]any, discount float64, cache map[string]*pricing.Entry) {
	// §3.2.1 — usage_details = a normalized copy of provided_usage_details (drop
	// negative/non-integer values; synthesize `total` from input+output if absent). We
	// do NOT derive usage (no tokenizer, and §7.3/R4 forbids phantom usage); no provided
	// usage → usage_details = {}.
	usage := normalizeUsage(p["provided_usage_details"])
	if len(usage) > 0 {
		p["usage_details"] = intMapToAny(usage)
	}

	// §4 step 1 — PROVIDED COST WINS and SHORT-CIRCUITS derivation (R1). The normalizer
	// already stamped provided_cost_details + cost_source=provided + total_cost; the
	// kernel-produced cost_details (§2) is a copy of the provided map, and no price entry
	// is consulted (pricing_snapshot_ref stays null).
	if pcd, ok := p["provided_cost_details"].(map[string]any); ok && len(pcd) > 0 {
		p["cost_details"] = copyAnyMap(pcd)
		p["cost_source"] = "provided"
		if _, has := p["total_cost"]; !has {
			p["total_cost"] = sumCostMap(pcd)
		}
		return
	}

	// §4 step 3 default (R4) — no usage → no derived cost. Leave cost_details/total_cost/
	// cost_source unset (null), never a fabricated zero.
	if len(usage) == 0 {
		return
	}
	model, _ := p["model"].(string)
	provider, _ := p["provider"].(string)
	if model == "" {
		return // no served model → no price lookup (§4 step 3)
	}

	entry := s.resolve(ctx, provider, model, spanTime(p), cache)
	if entry == nil {
		return // no price entry → cost null (§4 step 3), never zero
	}

	// §4 step 2 + §7 — derive, stamp derived cost + the re-derivable snapshot ref.
	costDetails, total := pricing.Derive(usage, entry, discount)
	if len(costDetails) == 0 {
		return
	}
	p["cost_details"] = floatMapToAny(costDetails)
	p["total_cost"] = total
	p["cost_source"] = "derived"
	p["pricing_snapshot_ref"] = map[string]any{
		"type":  "price",
		"id":    entry.ID,
		"label": entry.Provider + "/" + entry.Model + " v" + strconv.Itoa(entry.Version),
	}
}

// resolve looks up a price entry with a per-batch cache; a lookup error is fail-soft
// (returns nil → cost stays null, ingest is never failed).
func (s *enrichStage) resolve(ctx context.Context, provider, model string, at time.Time, cache map[string]*pricing.Entry) *pricing.Entry {
	// Cache by the CANONICAL key so two raw spellings that resolve to one entry share a
	// slot (the store canonicalizes internally either way — this only avoids a redundant
	// lookup, never a wrong cost).
	key := pricing.CanonicalProvider(provider) + "\x1e" + pricing.CanonicalModel(model)
	if e, ok := cache[key]; ok {
		return e
	}
	e, err := s.prices.Resolve(ctx, provider, model, at)
	if err != nil {
		if s.log != nil {
			s.log.Warn("enrich: price resolve failed; leaving cost null", "provider", provider, "model", model, "err", err.Error())
		}
		e = nil
	}
	cache[key] = e
	return e
}

// normalizeUsage returns the resolved usage as map[string]int64: non-negative integer
// values only, with `total` synthesized from input+output when absent (§3.2.1). Handles
// both int64 (native ingest) and float64 (JSON-round-tripped, e.g. plugin ingest) inputs.
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

func spanTime(p map[string]any) time.Time {
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
