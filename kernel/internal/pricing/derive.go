package pricing

import "sort"

// Derive computes cost_details (per-usage-key USD) and the scalar total for a span's
// RESOLVED usage against a price entry, applying the project discount. It is the money
// path — pure, data-driven, and the single place pricing rules become arithmetic:
//
//   - DATA-DRIVEN: a usage key is billed only if the entry carries a rate for it; the
//     rate is read off the entry, never a provider/bucket case list. Adding a provider
//     or a new detail key is price data, never code here.
//   - REDUCE FIRST: a base (input/output) is billed on its RESIDUAL
//     = base − Σ(bucket counts whose rate declares Reduces==base); each such bucket is
//     ALSO billed at its own rate. So input residual bills at the input rate while
//     cache_read/audio_input bill at their own rates — never double-charged, never folded
//     into input at the input rate.
//   - THEN TIER THE RESIDUAL: the base's tier breakpoint is applied to the RESIDUAL
//     (not the raw base — counting the specially-priced buckets toward the threshold
//     would double-count them), tax-bracket style (tokens up to the threshold at the
//     base rate, tokens above at the tier rate), never a cliff. One breakpoint per key
//     for now; graduated multi-breakpoint is a shape-ready follow-on.
//   - Discount: a factor in (0,1] multiplies every cost line.
//
// Returns an empty map + 0 when the entry is nil or usage is empty (the caller leaves
// cost null, never a fabricated zero). Costs are float64 (the model's
// cost_details type); the ClickHouse adapter stores Decimal64(12), so the documented
// cross-adapter bound is equality to 12 fractional digits.
func Derive(usage map[string]int64, e *Entry, discount float64) (map[string]float64, float64) {
	if e == nil || len(usage) == 0 {
		return map[string]float64{}, 0
	}
	cost := map[string]float64{}

	// 1. Base residuals (input, output): reduce first, then tier the residual.
	for _, base := range baseKeys {
		rate, ok := e.Rates[base]
		if !ok {
			continue // no base rate → not billed (data-driven)
		}
		residual := usage[base]
		for k, r := range e.Rates {
			if r.Reduces == base {
				residual -= usage[k] // usage[k] is 0 if the bucket is absent
			}
		}
		if residual < 0 {
			residual = 0 // clamp: a bucket count exceeding its base never yields negative cost
		}
		cost[base] = tieredCost(residual, rate.PerToken, tierFor(e, base))
	}

	// 2. Specially-priced detail buckets: any usage key (not a base, not `total`) that
	//    the entry prices, billed at its OWN rate. A bucket with Reduces was subtracted
	//    from its base above AND is billed here — that is the residual model, not a
	//    double-charge (the base paid only its residual).
	for k, cnt := range usage {
		if k == "input" || k == "output" || k == "total" {
			continue
		}
		r, ok := e.Rates[k]
		if !ok {
			continue // no rate → not billed
		}
		cost[k] = float64(cnt) * r.PerToken
	}

	// 3. Discount (a factor in (0,1]) multiplies every line; sum the scalar total in a
	//    STABLE key order so an identical span always derives a bit-identical total_cost
	//    (Go map iteration is randomized; float addition is order-dependent — an unsorted
	//    sum could differ by an ULP run-to-run, breaking re-delivery idempotency and the
	//    cross-adapter equality).
	factor := 1.0
	if discount > 0 && discount <= 1 {
		factor = discount
	}
	keys := make([]string, 0, len(cost))
	for k := range cost {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var total float64
	for _, k := range keys {
		cost[k] *= factor
		total += cost[k]
	}
	return cost, total
}

var baseKeys = []string{"input", "output"}

// tieredCost bills `residual` tokens at `baseRate`, applying a graduated single
// breakpoint if one is set: tokens up to the threshold at the base rate, tokens above
// at the tier rate (tax-bracket, never a cliff).
func tieredCost(residual int64, baseRate float64, tier *Tier) float64 {
	if tier == nil || residual <= tier.ThresholdTokens {
		return float64(residual) * baseRate
	}
	return float64(tier.ThresholdTokens)*baseRate + float64(residual-tier.ThresholdTokens)*tier.PerToken
}

// tierFor returns the entry's tier for a usage key, or nil. Honors a single
// breakpoint per key; if more than one is present the first is used (graduated
// multi-breakpoint is the shape-ready follow-on).
func tierFor(e *Entry, key string) *Tier {
	for i := range e.Tiers {
		if e.Tiers[i].Key == key {
			return &e.Tiers[i]
		}
	}
	return nil
}
