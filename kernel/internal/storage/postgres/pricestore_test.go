package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/pricing"
)

// setupPrices connects to the test Postgres, migrates, and wipes the price tables.
func setupPrices(t *testing.T) (*PriceStore, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("LLMOBS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set LLMOBS_TEST_DATABASE_URL to run the price-store tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM price_entries`)
	_, _ = pool.Exec(ctx, `DELETE FROM price_discounts`)
	return NewPriceStore(pool), pool
}

// TestPriceStoreAppendVersioning proves the ADR-0029 core: an edit appends a new
// version (prior versions retained), Resolve returns the newest applicable version,
// and GetByID resolves the exact historical entry a snapshot ref points at.
func TestPriceStoreAppendVersioning(t *testing.T) {
	s, _ := setupPrices(t)
	ctx := context.Background()

	// v1: gpt-4o at $2.50/Mtok input, effective 2026-01-01.
	v1, err := s.Upsert(ctx, pricing.Entry{
		Provider: "openai", Model: "gpt-4o", EffectiveFrom: "2026-01-01T00:00:00Z",
		Rates: map[string]pricing.Rate{"input": {PerToken: 0.0000025}, "output": {PerToken: 0.00001}},
	}, "session:admin@x")
	if err != nil {
		t.Fatalf("upsert v1: %v", err)
	}
	if v1.Version != 1 || v1.ID != "openai/gpt-4o#1" {
		t.Fatalf("v1 = %+v", v1)
	}

	// A price correction: a NEW version, effective later. v1 must be retained.
	v2, err := s.Upsert(ctx, pricing.Entry{
		Provider: "openai", Model: "gpt-4o", EffectiveFrom: "2026-06-01T00:00:00Z",
		Rates: map[string]pricing.Rate{"input": {PerToken: 0.000002}, "output": {PerToken: 0.000008}},
	}, "session:admin@x")
	if err != nil {
		t.Fatalf("upsert v2: %v", err)
	}
	if v2.Version != 2 || v2.ID != "openai/gpt-4o#2" {
		t.Fatalf("v2 = %+v", v2)
	}

	// Resolve is time-correct: a span in Feb sees v1; a span in July sees v2.
	feb, _ := time.Parse(time.RFC3339, "2026-02-15T00:00:00Z")
	jul, _ := time.Parse(time.RFC3339, "2026-07-15T00:00:00Z")
	if e, _ := s.Resolve(ctx, "openai", "gpt-4o", feb); e == nil || e.Version != 1 {
		t.Fatalf("Feb should resolve v1, got %+v", e)
	}
	if e, _ := s.Resolve(ctx, "openai", "gpt-4o", jul); e == nil || e.Version != 2 {
		t.Fatalf("July should resolve v2, got %+v", e)
	}

	// The old version is still retrievable by its snapshot-ref id (re-derivability).
	if e, _ := s.GetByID(ctx, "openai/gpt-4o#1"); e == nil || e.Rates["input"].PerToken != 0.0000025 {
		t.Fatalf("historical v1 must remain retrievable by id, got %+v", e)
	}

	// A model with no entry resolves to nil (caller leaves cost null, never zero).
	if e, _ := s.Resolve(ctx, "openai", "no-such-model", jul); e != nil {
		t.Fatalf("unknown model must resolve nil, got %+v", e)
	}
}

// TestPriceStoreResolveSymmetricNormalization is the R6 proof against a real DB: an
// entry seeded under a canonical key resolves from a prefixed/aliased raw lookup.
func TestPriceStoreResolveSymmetricNormalization(t *testing.T) {
	s, _ := setupPrices(t)
	ctx := context.Background()
	if _, err := s.Upsert(ctx, pricing.Entry{
		Provider: "vertex_ai", Model: "gemini-1.5-pro", EffectiveFrom: "2020-01-01T00:00:00Z",
		Rates: map[string]pricing.Rate{"input": {PerToken: 0.00000125}},
	}, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	now := time.Now()
	// Stored canonical as (google, gemini-1.5-pro); every alias/prefix must resolve it.
	for _, tc := range []struct{ prov, model string }{
		{"vertex_ai", "gemini-1.5-pro"},
		{"gemini", "google/gemini-1.5-pro"},
		{"google_ai", "vertex_ai/gemini-1.5-pro"},
		{"GOOGLE", "gemini-1.5-pro"},
	} {
		e, err := s.Resolve(ctx, tc.prov, tc.model, now)
		if err != nil {
			t.Fatalf("resolve %v: %v", tc, err)
		}
		if e == nil {
			t.Fatalf("R6 miss: (%s,%s) did not resolve the canonical google/gemini-1.5-pro entry", tc.prov, tc.model)
		}
	}
}

// TestPriceStoreDiscount proves per-project discount get/set + the (0,1] guard.
func TestPriceStoreDiscount(t *testing.T) {
	s, pool := setupPrices(t)
	ctx := context.Background()
	// A project row is required (FK). Reuse/create a throwaway.
	_, _ = pool.Exec(ctx, `INSERT INTO organizations (id,name) VALUES ('o-price','x') ON CONFLICT DO NOTHING`)
	_, _ = pool.Exec(ctx, `INSERT INTO projects (id,org_id,name) VALUES ('p-price','o-price','x') ON CONFLICT DO NOTHING`)

	if _, ok, _ := s.GetDiscount(ctx, "p-price"); ok {
		t.Fatal("unset discount must report not-present")
	}
	if err := s.SetDiscount(ctx, "p-price", 0.8, "session:admin@x"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if f, ok, _ := s.GetDiscount(ctx, "p-price"); !ok || f != 0.8 {
		t.Fatalf("discount = %v,%v", f, ok)
	}
	// A markup (factor > 1) or non-positive factor is rejected.
	for _, bad := range []float64{0, -0.5, 1.5} {
		if err := s.SetDiscount(ctx, "p-price", bad, ""); err == nil {
			t.Fatalf("factor %v must be rejected", bad)
		}
	}
}

// TestSeedDefaultsIdempotent proves seeding twice does not create duplicate versions
// and never clobbers an operator's newer version.
func TestSeedDefaultsIdempotent(t *testing.T) {
	s, _ := setupPrices(t)
	ctx := context.Background()
	if err := s.SeedDefaults(ctx); err != nil {
		t.Fatalf("seed 1: %v", err)
	}
	// Operator edits gpt-4o → v2.
	if _, err := s.Upsert(ctx, pricing.Entry{
		Provider: "openai", Model: "gpt-4o", EffectiveFrom: "2026-06-01T00:00:00Z",
		Rates: map[string]pricing.Rate{"input": {PerToken: 0.000001}},
	}, "session:admin@x"); err != nil {
		t.Fatalf("edit: %v", err)
	}
	// Re-seed (next boot). Must NOT re-insert v1 as a new row nor touch v2.
	if err := s.SeedDefaults(ctx); err != nil {
		t.Fatalf("seed 2: %v", err)
	}
	vers, err := s.ListVersions(ctx, "openai", "gpt-4o")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(vers) != 2 {
		t.Fatalf("re-seed must not duplicate versions; want 2, got %d", len(vers))
	}
	// Resolve today still sees the operator's v2 (re-seed did not clobber it).
	if e, _ := s.Resolve(ctx, "openai", "gpt-4o", time.Now()); e == nil || e.Version != 2 {
		t.Fatalf("operator v2 must win after re-seed, got %+v", e)
	}
}

// TestListCurrentIsEffectiveNow proves ListCurrent shows the CURRENTLY-billed version
// (Resolve(now)), not merely the highest version — a future-dated correction is not yet
// "current" (the review's ListCurrent/Resolve divergence).
func TestListCurrentIsEffectiveNow(t *testing.T) {
	s, _ := setupPrices(t)
	ctx := context.Background()
	if _, err := s.Upsert(ctx, pricing.Entry{
		Provider: "openai", Model: "gpt-4o", EffectiveFrom: "2020-01-01T00:00:00Z",
		Rates: map[string]pricing.Rate{"input": {PerToken: 0.0000025}},
	}, "session:a"); err != nil {
		t.Fatal(err)
	}
	// A future-dated v2 (effective next year): highest version, but NOT yet effective.
	if _, err := s.Upsert(ctx, pricing.Entry{
		Provider: "openai", Model: "gpt-4o", EffectiveFrom: "2099-01-01T00:00:00Z",
		Rates: map[string]pricing.Rate{"input": {PerToken: 0.000001}},
	}, "session:a"); err != nil {
		t.Fatal(err)
	}
	cur, err := s.ListCurrent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cur) != 1 || cur[0].Version != 1 {
		t.Fatalf("ListCurrent must show the effective v1 (not the future v2), got %+v", cur)
	}
}

// TestUpsertForcesOverrideSource proves an operator write cannot spoof 'default'
// provenance (L-1): the stored source is always 'override' regardless of the body.
func TestUpsertForcesOverrideSource(t *testing.T) {
	s, _ := setupPrices(t)
	ctx := context.Background()
	e, err := s.Upsert(ctx, pricing.Entry{
		Provider: "openai", Model: "gpt-4o", Source: "default", EffectiveFrom: "2026-01-01T00:00:00Z",
		Rates: map[string]pricing.Rate{"input": {PerToken: 0.0000025}},
	}, "session:a")
	if err != nil {
		t.Fatal(err)
	}
	if e.Source != "override" {
		t.Fatalf("an API write must be stored as 'override', got %q", e.Source)
	}
}

// TestUpsertRejectsBadRates proves the money-integrity guard rejects a negative rate.
func TestUpsertRejectsBadRates(t *testing.T) {
	s, _ := setupPrices(t)
	ctx := context.Background()
	if _, err := s.Upsert(ctx, pricing.Entry{
		Provider: "openai", Model: "gpt-4o", EffectiveFrom: "2026-01-01T00:00:00Z",
		Rates: map[string]pricing.Rate{"input": {PerToken: -0.5}},
	}, "session:a"); err == nil {
		t.Fatal("a negative rate must be rejected at the persist seam")
	}
}

// TestDefaultSeedsValid proves every shipped default seed passes validation and yields
// a schema-valid id (a bad seed can never ship).
func TestDefaultSeedsValid(t *testing.T) {
	for _, e := range defaultPriceSeeds() {
		if err := e.ValidateRates(); err != nil {
			t.Errorf("default seed %s/%s invalid: %v", e.Provider, e.Model, err)
		}
	}
}

// TestResolveDotDashFallback is the #106 prove-the-negative: a dotted model variant
// ("claude-3.5-sonnet") still prices against a dashed stored entry ("claude-3-5-sonnet")
// instead of silently deriving NO cost — while the EXACT match still wins (R6 primary path)
// and a genuinely different model does NOT false-match.
func TestResolveDotDashFallback(t *testing.T) {
	s, _ := setupPrices(t)
	ctx := context.Background()
	at := mustTime(t, "2026-06-01T00:00:00Z")

	// Stored with DASHES.
	if _, err := s.Upsert(ctx, pricing.Entry{
		Provider: "anthropic", Model: "claude-3-5-sonnet", EffectiveFrom: "2026-01-01T00:00:00Z",
		Rates: map[string]pricing.Rate{"input": {PerToken: 0.000003}, "output": {PerToken: 0.000015}},
	}, "session:admin@x"); err != nil {
		t.Fatal(err)
	}

	// Queried with DOTS → must resolve via the fallback (not nil).
	e, err := s.Resolve(ctx, "anthropic", "claude-3.5-sonnet", at)
	if err != nil {
		t.Fatal(err)
	}
	if e == nil || e.Model != "claude-3-5-sonnet" {
		t.Fatalf("dotted variant must price against the dashed entry, got %v", e)
	}
	// Exact (dashed) still resolves — the primary path is unchanged.
	if e2, _ := s.Resolve(ctx, "anthropic", "claude-3-5-sonnet", at); e2 == nil {
		t.Fatal("exact model must still resolve")
	}

	// Reverse direction: store DOTS, query DASHES.
	if _, err := s.Upsert(ctx, pricing.Entry{
		Provider: "openai", Model: "gpt-4.1", EffectiveFrom: "2026-01-01T00:00:00Z",
		Rates: map[string]pricing.Rate{"input": {PerToken: 0.000002}},
	}, "session:admin@x"); err != nil {
		t.Fatal(err)
	}
	if e3, _ := s.Resolve(ctx, "openai", "gpt-4-1", at); e3 == nil || e3.Model != "gpt-4.1" {
		t.Fatalf("dashed query must price against the dotted entry, got %v", e3)
	}

	// A genuinely different model (no dot/dash coincidence) must NOT match.
	if e4, _ := s.Resolve(ctx, "anthropic", "claude-3-opus", at); e4 != nil {
		t.Fatalf("an unrelated model must not false-match, got %v", e4)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return tt
}
