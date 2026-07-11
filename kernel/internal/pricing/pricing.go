// Package pricing is the cost-derivation domain: the price-entry model and the ONE
// canonical model-key + provider normalizer shared by price-table load and derivation
// lookup (ADR-0029; 06-usage-cost.md §7). Keeping normalization in a single place — and
// applying it byte-identically at both write and lookup — is the R6 immunity: an
// asymmetric transform makes every lookup miss and silently records zero cost
// (Opik #5621). The storage adapter (postgres.PriceStore) and the enrich stage (M2)
// both key on these canonical values.
package pricing

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ErrVersionConflict is returned when a concurrent price edit raced to the same
// version. Lives here so the storage adapter (which returns it) and the control-plane
// API (which maps it to 409) share one sentinel.
var ErrVersionConflict = errors.New("price entry version conflict (retry)")

// Rate is one usage-detail bucket's unit price (§7.4/R4). Reduces, when set, declares
// which base count this bucket's tokens are subtracted from for the residual (§7.3/R3);
// empty means a standalone additive bucket that reduces no base.
type Rate struct {
	PerToken float64 `json:"per_token"`
	Reduces  string  `json:"reduces,omitempty"` // "input" | "output" | ""
}

// Tier is one above-threshold rate (§7.5/R7): tokens of Key above ThresholdTokens
// bill at PerToken instead of the base rate.
type Tier struct {
	Key             string  `json:"key"`
	ThresholdTokens int64   `json:"threshold_tokens"`
	PerToken        float64 `json:"per_token"`
}

// Entry is one immutable, versioned pricing snapshot for a (Provider, Model). It
// mirrors api/schemas/pricing/v1alpha1/price-entry.schema.json. ID
// ("<provider>/<model>#<version>") is the primary key AND the pricing_snapshot_ref id
// (§5/D4). Provider and Model are stored canonical (via Canonical* below).
type Entry struct {
	ID            string          `json:"id"`
	Provider      string          `json:"provider"`
	Model         string          `json:"model"`
	Version       int             `json:"version"`
	EffectiveFrom string          `json:"effective_from"` // RFC3339
	Rates         map[string]Rate `json:"rates"`
	Tiers         []Tier          `json:"tiers"`
	Source        string          `json:"source"` // "default" | "override"
	RawProvider   string          `json:"raw_provider,omitempty"`
	CreatedBy     string          `json:"created_by,omitempty"`
	CreatedAt     string          `json:"created_at,omitempty"` // RFC3339
}

// ValidateRates rejects money-integrity landmines in an entry's rates/tiers: a rate
// must be finite and non-negative (a negative rate would produce negative cost; NaN/Inf
// would poison every aggregate), a `reduces` must name a real base, and a tier must
// have a positive threshold. Called at the write seam so bad price data never lands.
func (e Entry) ValidateRates() error {
	for k, r := range e.Rates {
		if !finiteNonNeg(r.PerToken) {
			return fmt.Errorf("rate %q: per_token must be a finite, non-negative number", k)
		}
		if r.Reduces != "" && r.Reduces != "input" && r.Reduces != "output" {
			return fmt.Errorf("rate %q: reduces must be \"input\", \"output\", or absent", k)
		}
	}
	for i, t := range e.Tiers {
		if t.Key == "" {
			return fmt.Errorf("tier %d: key is required", i)
		}
		if t.ThresholdTokens <= 0 {
			return fmt.Errorf("tier %d: threshold_tokens must be positive", i)
		}
		if !finiteNonNeg(t.PerToken) {
			return fmt.Errorf("tier %d: per_token must be a finite, non-negative number", i)
		}
	}
	return nil
}

func finiteNonNeg(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) && f >= 0 }

// EntryID builds the canonical primary key / snapshot-ref id for a (provider, model,
// version). Callers pass ALREADY-canonical provider/model (this does not normalize, so
// the id is stable regardless of how the inputs were derived).
func EntryID(providerCanon, modelCanon string, version int) string {
	return providerCanon + "/" + modelCanon + "#" + strconv.Itoa(version)
}

// providerCanon collapses provider aliases to ONE identity (R6). The Google family
// (Vertex AI / Gemini / Google AI) is the load-bearing case both incumbents got wrong
// (Opik #6928). Unlisted providers pass through lowercased+trimmed — pricing is
// data-driven, so a new provider is a new price row, never a code change here.
var providerCanon = map[string]string{
	"vertex_ai": "google", "vertexai": "google", "vertex-ai": "google",
	"gemini": "google", "google_ai": "google", "google-ai": "google",
	"google_genai": "google", "googleai": "google", "google": "google",
	"azure": "azure", "azure_openai": "azure", "azure-openai": "azure", "azureopenai": "azure",
	"openai": "openai", "anthropic": "anthropic", "claude": "anthropic",
	"aws": "bedrock", "bedrock": "bedrock", "amazon_bedrock": "bedrock", "amazon-bedrock": "bedrock",
}

// CanonicalProvider maps a raw provider identifier to its canonical identity. Applied
// identically wherever a provider is keyed (seed, edit, lookup).
func CanonicalProvider(raw string) string {
	p := strings.ToLower(strings.TrimSpace(raw))
	if c, ok := providerCanon[p]; ok {
		return c
	}
	return p
}

// modelPrefixStrip is the set of leading path segments in a model name that denote a
// provider or router, not the model itself, and are stripped so a prefixed name
// resolves to the same key as the bare name (R6). Kept in sync with providerCanon's
// intent; unlisted segments are preserved (they are part of the model).
var modelPrefixStrip = map[string]bool{
	"openrouter": true, "openai": true, "anthropic": true, "google": true,
	"vertex_ai": true, "vertexai": true, "vertex-ai": true, "gemini": true,
	"google_ai": true, "google-ai": true, "google_genai": true,
	"azure": true, "azure_openai": true, "azure-openai": true,
	"bedrock": true, "aws": true, "amazon": true,
	"mistral": true, "mistralai": true, "cohere": true, "meta": true, "meta-llama": true,
	"deepseek": true, "fireworks": true, "together": true, "togethercomputer": true,
	"groq": true, "perplexity": true, "xai": true, "x-ai": true,
}

// CanonicalModel strips provider/router prefix segments and lowercases, so
// "openai/gpt-4o", "openrouter/openai/gpt-4o", and "gpt-4o" all resolve to "gpt-4o".
// Byte-identical at load and lookup (R6). A model name whose leading segment is not a
// known prefix is preserved verbatim (it is part of the model).
func CanonicalModel(raw string) string {
	m := strings.ToLower(strings.TrimSpace(raw))
	if m == "" {
		return ""
	}
	parts := strings.Split(m, "/")
	i := 0
	for i < len(parts)-1 && modelPrefixStrip[parts[i]] {
		i++
	}
	return strings.Join(parts[i:], "/")
}
