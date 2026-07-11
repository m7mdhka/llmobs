package postgres

import "github.com/m7mdhka/llmobs/kernel/internal/pricing"

// defaultPriceSeeds is a reasonable baseline price set (per-token USD) for common
// models, so cost derivation works out of the box. This is a STARTING POINT, not
// authoritative — the point of the price table (ADR-0029) is that an operator corrects
// or extends it via the API without a code change. Rates are illustrative and dated;
// keep the list short and canonical. Provider/model are canonicalized on seed.
//
// Detail-rate `Reduces` declares the residual base (R3): cache_read tokens come out of
// the input count, so the base input rate bills only `input − cache_read`.
func defaultPriceSeeds() []pricing.Entry {
	return []pricing.Entry{
		{
			Provider: "openai", Model: "gpt-4o",
			Rates: map[string]pricing.Rate{
				"input":      {PerToken: 0.0000025},
				"output":     {PerToken: 0.00001},
				"cache_read": {PerToken: 0.00000125, Reduces: "input"},
			},
		},
		{
			Provider: "openai", Model: "gpt-4o-mini",
			Rates: map[string]pricing.Rate{
				"input":      {PerToken: 0.00000015},
				"output":     {PerToken: 0.0000006},
				"cache_read": {PerToken: 0.000000075, Reduces: "input"},
			},
		},
		{
			Provider: "anthropic", Model: "claude-3-5-sonnet-20241022",
			Rates: map[string]pricing.Rate{
				"input":       {PerToken: 0.000003},
				"output":      {PerToken: 0.000015},
				"cache_read":  {PerToken: 0.0000003, Reduces: "input"},
				"cache_write": {PerToken: 0.00000375, Reduces: "input"},
			},
		},
		{
			Provider: "google", Model: "gemini-1.5-pro",
			Rates: map[string]pricing.Rate{
				"input":      {PerToken: 0.00000125},
				"output":     {PerToken: 0.000005},
				"cache_read": {PerToken: 0.0000003125, Reduces: "input"},
			},
			// Long-context tier (§7.5/R7): tokens above 128k bill at the higher rate.
			Tiers: []pricing.Tier{
				{Key: "input", ThresholdTokens: 128000, PerToken: 0.0000025},
				{Key: "output", ThresholdTokens: 128000, PerToken: 0.00001},
			},
		},
	}
}
