package pricing

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

// entry is a small builder for readable test cases.
func entry(rates map[string]Rate, tiers ...Tier) *Entry {
	return &Entry{Rates: rates, Tiers: tiers}
}

// TestDeriveResidualEachBucketOwnRate is the residual general-form proof: a
// call with cache + reasoning + audio buckets prices EACH at its own rate, bills the
// base residual (input − cache − audio_input; output − reasoning) at the base rate, and
// double-charges NOTHING. This is the case both incumbents got wrong in opposite
// directions (Langfuse over-subtracted, Opik under-discounted).
func TestDeriveResidualEachBucketOwnRate(t *testing.T) {
	e := entry(map[string]Rate{
		"input":       {PerToken: 10}, // absurd round rates for exact arithmetic
		"output":      {PerToken: 20},
		"cache_read":  {PerToken: 1, Reduces: "input"},
		"audio_input": {PerToken: 5, Reduces: "input"},
		"reasoning":   {PerToken: 4, Reduces: "output"},
	})
	usage := map[string]int64{
		"input":       1000, // residual = 1000 − 100(cache) − 50(audio_in) = 850
		"output":      400,  // residual = 400 − 60(reasoning) = 340
		"cache_read":  100,
		"audio_input": 50,
		"reasoning":   60,
	}
	cost, total := Derive(usage, e, 0)
	want := map[string]float64{
		"input":       850 * 10.0, // 8500
		"output":      340 * 20.0, // 6800
		"cache_read":  100 * 1.0,  // 100
		"audio_input": 50 * 5.0,   // 250
		"reasoning":   60 * 4.0,   // 240
	}
	for k, w := range want {
		if !approx(cost[k], w) {
			t.Errorf("cost[%q]=%v want %v", k, cost[k], w)
		}
	}
	wantTotal := 8500.0 + 6800 + 100 + 250 + 240
	if !approx(total, wantTotal) {
		t.Fatalf("total=%v want %v", total, wantTotal)
	}
	// The base was billed on its residual, NOT the raw count: input at raw 1000 would be
	// 10000, not 8500 — proving cache+audio were not double-charged at the input rate.
	if approx(cost["input"], 1000*10.0) {
		t.Fatal("input billed on the RAW count — buckets were double-charged (the incumbent bug)")
	}
}

// TestDeriveReduceThenTierCrossover is the reduce-then-tier REQUIRED fixture: a call with a cache
// bucket AND a tier breakpoint, asserting the tier applies to the RESIDUAL, with the
// just-under / just-over crossover computing correctly. Tiering the RAW input would
// wrongly cross the breakpoint (the double-count that reduce-first forbids).
func TestDeriveReduceThenTierCrossover(t *testing.T) {
	// input tier: tokens above 200k bill at 2 (base 10); cache_read reduces input.
	e := entry(map[string]Rate{
		"input":      {PerToken: 10},
		"cache_read": {PerToken: 1, Reduces: "input"},
	}, Tier{Key: "input", ThresholdTokens: 200_000, PerToken: 2})

	// (i) raw input 210k, cache 30k → residual 180k, which is BELOW 200k: tier does NOT
	// apply. Billing raw 210k would wrongly cross. residual*base = 180000*10 = 1.8e6.
	cost, _ := Derive(map[string]int64{"input": 210_000, "cache_read": 30_000}, e, 0)
	if !approx(cost["input"], 180_000*10) {
		t.Fatalf("just-under: input=%v want %v (tier must NOT apply to the 180k residual)", cost["input"], 180_000*10.0)
	}
	if !approx(cost["cache_read"], 30_000*1) {
		t.Fatalf("cache_read=%v want %v", cost["cache_read"], 30_000*1.0)
	}

	// (ii) raw input 260k, cache 30k → residual 230k, ABOVE 200k by 30k: graduated —
	// 200k at base(10) + 30k at tier(2) = 2,000,000 + 60,000 = 2,060,000.
	cost2, _ := Derive(map[string]int64{"input": 260_000, "cache_read": 30_000}, e, 0)
	wantOver := 200_000*10.0 + 30_000*2.0
	if !approx(cost2["input"], wantOver) {
		t.Fatalf("just-over: input=%v want %v (graduated on the 230k residual)", cost2["input"], wantOver)
	}
	// Cliff check: a whole-amount reprice at the tier rate (230k*2=460k) is WRONG.
	if approx(cost2["input"], 230_000*2.0) {
		t.Fatal("tier applied as a CLIFF (whole amount at tier rate) — must be graduated")
	}
}

// TestDeriveGraduatedContinuousAtBoundary proves the graduated tier has no
// discontinuous jump at the breakpoint (a cliff would jump).
func TestDeriveGraduatedContinuousAtBoundary(t *testing.T) {
	e := entry(map[string]Rate{"input": {PerToken: 10}}, Tier{Key: "input", ThresholdTokens: 1000, PerToken: 2})
	at, _ := Derive(map[string]int64{"input": 1000}, e, 0)   // exactly at: 1000*10
	over, _ := Derive(map[string]int64{"input": 1001}, e, 0) // one over: 1000*10 + 1*2
	if !approx(at["input"], 10000) || !approx(over["input"], 10002) {
		t.Fatalf("boundary not continuous: at=%v over=%v", at["input"], over["input"])
	}
}

// TestDeriveDataDrivenNoRateNoCost is the data-driven rule: a usage key with NO rate on the entry is
// not billed (no provider case list); a provider whose entry simply lacks a cache rate
// is not cache-priced — with zero code branching on provider.
func TestDeriveDataDrivenNoRateNoCost(t *testing.T) {
	// Entry prices input+output only; a cache_read count present but UNPRICED.
	e := entry(map[string]Rate{"input": {PerToken: 10}, "output": {PerToken: 20}})
	cost, total := Derive(map[string]int64{"input": 100, "output": 50, "cache_read": 999}, e, 0)
	if _, billed := cost["cache_read"]; billed {
		t.Fatal("an unpriced bucket must not be billed (data-driven)")
	}
	// input has no reduces-bucket priced, so residual == raw 100.
	if !approx(cost["input"], 1000) || !approx(cost["output"], 1000) {
		t.Fatalf("cost=%v", cost)
	}
	if !approx(total, 2000) {
		t.Fatalf("total=%v want 2000", total)
	}
}

// TestDeriveDiscount proves the per-project discount multiplies every line.
func TestDeriveDiscount(t *testing.T) {
	e := entry(map[string]Rate{"input": {PerToken: 10}, "output": {PerToken: 20}})
	cost, total := Derive(map[string]int64{"input": 100, "output": 100}, e, 0.5)
	if !approx(cost["input"], 500) || !approx(cost["output"], 1000) || !approx(total, 1500) {
		t.Fatalf("discounted cost=%v total=%v", cost, total)
	}
	// A zero/out-of-range discount is treated as none (never inflates).
	_, full := Derive(map[string]int64{"input": 100}, e, 0)
	if !approx(full, 1000) {
		t.Fatalf("no-discount total=%v want 1000", full)
	}
}

// TestDeriveEmptyNilSafe: no usage or no entry → empty map, 0 total (caller leaves null).
func TestDeriveEmptyNilSafe(t *testing.T) {
	if c, tot := Derive(nil, entry(map[string]Rate{"input": {PerToken: 10}}), 0); len(c) != 0 || tot != 0 {
		t.Fatal("empty usage must derive nothing")
	}
	if c, tot := Derive(map[string]int64{"input": 100}, nil, 0); len(c) != 0 || tot != 0 {
		t.Fatal("nil entry must derive nothing (leave cost null, never zero)")
	}
}

// TestDeriveResidualClampsNonNegative: a bucket count exceeding its base never yields
// negative cost (provider semantics vary; we must not assume inclusive/exclusive).
func TestDeriveResidualClampsNonNegative(t *testing.T) {
	e := entry(map[string]Rate{"input": {PerToken: 10}, "cache_read": {PerToken: 1, Reduces: "input"}})
	cost, _ := Derive(map[string]int64{"input": 100, "cache_read": 500}, e, 0)
	if cost["input"] < 0 {
		t.Fatalf("residual must clamp at 0, got input cost %v", cost["input"])
	}
	if !approx(cost["input"], 0) {
		t.Fatalf("residual 100-500 clamps to 0 → input cost 0, got %v", cost["input"])
	}
	if !approx(cost["cache_read"], 500) {
		t.Fatalf("cache_read still billed at its rate, got %v", cost["cache_read"])
	}
}
