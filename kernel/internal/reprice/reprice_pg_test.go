package reprice

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/costderive"
	"github.com/m7mdhka/llmobs/kernel/internal/pricing"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
)

// These integration proofs run against a real Postgres (the money mutation path must be
// proven on the real fold + real SQL, not a fake). They exercise the acceptance bar:
// a price correction re-prices spans deterministically to the new version + a fresh
// snapshot ref; provided-cost spans are never touched; a second identical run is a
// no-op (idempotent); and a project-scoped (discount) re-price provably cannot cross a
// tenant boundary.

func pgSetup(t *testing.T) (*postgres.Store, *postgres.PriceStore, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("LLMOBS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set LLMOBS_TEST_DATABASE_URL to run the re-pricing integration proofs")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Org/projects for the two tenants + a clean slate.
	_, _ = pool.Exec(ctx, `INSERT INTO organizations (id, name) VALUES ('o-rp','x') ON CONFLICT DO NOTHING`)
	for _, p := range []string{"rp-a", "rp-b"} {
		if _, err := pool.Exec(ctx, `INSERT INTO projects (id, org_id, name) VALUES ($1,'o-rp',$1) ON CONFLICT DO NOTHING`, p); err != nil {
			t.Fatalf("project %s: %v", p, err)
		}
	}
	for _, stmt := range []string{
		`DELETE FROM spans WHERE project_id IN ('rp-a','rp-b')`,
		`DELETE FROM price_entries WHERE provider='openai' AND model='gpt-4o'`,
		`DELETE FROM price_discounts WHERE project_id IN ('rp-a','rp-b')`,
		`DELETE FROM reprice_state WHERE run_key LIKE '%rp-%' OR run_key LIKE '%openai/gpt-4o%'`,
		`DELETE FROM reprice_deadletter WHERE run_key LIKE '%rp-%' OR run_key LIKE '%openai/gpt-4o%'`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("clean: %v", err)
		}
	}
	return postgres.NewStore(pool), postgres.NewPriceStore(pool), pool
}

// upsertV1 / upsertV2 create the price schedule: v2 is a CORRECTION (same effective_from,
// so it supersedes v1 for the span's time — Resolve returns the higher version).
func upsertV1(t *testing.T, ps *postgres.PriceStore) {
	t.Helper()
	if _, err := ps.Upsert(context.Background(), pricing.Entry{
		Provider: "openai", Model: "gpt-4o", EffectiveFrom: "2026-01-01T00:00:00Z",
		Rates: map[string]pricing.Rate{"input": {PerToken: 0.0000025}, "output": {PerToken: 0.00001}},
	}, "session:admin@x"); err != nil {
		t.Fatalf("upsert v1: %v", err)
	}
}

func upsertV2(t *testing.T, ps *postgres.PriceStore) {
	t.Helper()
	if _, err := ps.Upsert(context.Background(), pricing.Entry{
		Provider: "openai", Model: "gpt-4o", EffectiveFrom: "2026-01-01T00:00:00Z",
		Rates: map[string]pricing.Rate{"input": {PerToken: 0.000002}, "output": {PerToken: 0.000008}},
	}, "session:admin@x"); err != nil {
		t.Fatalf("upsert v2: %v", err)
	}
}

// seedDerivedSpan ingests a span exactly as the enrich stage would: derive cost against
// the CURRENT price table (via the shared costderive path), then persist. Returns the id.
func seedDerivedSpan(t *testing.T, st *postgres.Store, ps *postgres.PriceStore, project, id string) {
	t.Helper()
	ctx := context.Background()
	p := map[string]any{
		"project_id": project, "id": id, "trace_id": "t-" + id, "kind": "generation",
		"model": "gpt-4o", "provider": "openai",
		"start_time":             "2026-06-01T12:00:00Z",
		"provided_usage_details": map[string]any{"input": 1000.0, "output": 500.0},
	}
	discount, _, _ := ps.GetDiscount(ctx, project)
	entry, err := costderive.DeriveSpanCost(ctx, p, ps, discount, map[string]*pricing.Entry{})
	if err != nil || entry == nil {
		t.Fatalf("seed derive %s: entry=%v err=%v", id, entry, err)
	}
	if err := st.PersistSpan(ctx, storage.Event{
		Op: storage.OpUpsert, EventTS: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC), EventID: id + "-ingest", Payload: p,
	}); err != nil {
		t.Fatalf("seed persist %s: %v", id, err)
	}
}

// seedProvidedSpan ingests a span whose cost was PROVIDED. It carries no
// pricing_snapshot_ref, so re-pricing must never touch it.
func seedProvidedSpan(t *testing.T, st *postgres.Store, project, id string) {
	t.Helper()
	ctx := context.Background()
	p := map[string]any{
		"project_id": project, "id": id, "trace_id": "t-" + id, "kind": "generation",
		"model": "gpt-4o", "provider": "openai", "start_time": "2026-06-01T12:00:00Z",
		"cost_source":  "provided",
		"cost_details": map[string]any{"input": 0.99, "total": 0.99},
		"total_cost":   0.99,
	}
	if err := st.PersistSpan(ctx, storage.Event{
		Op: storage.OpUpsert, EventTS: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC), EventID: id + "-ingest", Payload: p,
	}); err != nil {
		t.Fatalf("seed provided %s: %v", id, err)
	}
}

func readCost(t *testing.T, st *postgres.Store, project, id string) (total float64, refID string) {
	t.Helper()
	doc, err := st.GetSpan(context.Background(), project, id)
	if err != nil || doc == nil {
		t.Fatalf("get %s: doc=%v err=%v", id, doc, err)
	}
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", id, err)
	}
	total, _ = m["total_cost"].(float64)
	if ref, ok := m["pricing_snapshot_ref"].(map[string]any); ok {
		refID, _ = ref["id"].(string)
	}
	return total, refID
}

func newRunner(st *postgres.Store, ps *postgres.PriceStore) *Runner {
	// A fixed future clock: the re-price event must beat the span's ingest event_ts so the
	// new cost wins the latest-wins cost group. Fixed → deterministic (no wall-clock flake).
	fixed := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	return New(st, st, ps, Config{ChunkSize: 1, Now: func() time.Time { return fixed }}, nil)
}

// TestReprice_PriceCorrection is the headline proof: edit a price → new version → run the
// backfill → the span carries the NEW cost AND a NEW snapshot ref pointing at v2; a
// provided-cost span is untouched; a second identical run is a no-op.
func TestReprice_PriceCorrection(t *testing.T) {
	st, ps, _ := pgSetup(t)
	ctx := context.Background()

	upsertV1(t, ps)
	seedDerivedSpan(t, st, ps, "rp-a", "s-derived")
	seedProvidedSpan(t, st, "rp-a", "s-provided")

	// Ingest priced at v1: input 1000*2.5e-6 + output 500*1e-5 = 0.0025 + 0.005 = 0.0075.
	if total, ref := readCost(t, st, "rp-a", "s-derived"); total != 0.0075 || ref != "openai/gpt-4o#1" {
		t.Fatalf("v1 ingest cost = %v ref %q, want 0.0075 / #1", total, ref)
	}

	// A price correction: v2 supersedes v1 for the span's time.
	upsertV2(t, ps)

	r := newRunner(st, ps)
	res, err := r.Run(ctx, storage.RepriceFilter{SnapshotRefID: "openai/gpt-4o#1"})
	if err != nil {
		t.Fatalf("reprice run: %v", err)
	}
	if !res.Complete || res.Repriced != 1 || res.Scanned != 1 {
		t.Fatalf("run result = %+v, want complete/repriced=1/scanned=1", res)
	}

	// v2 cost: 1000*2e-6 + 500*8e-6 = 0.002 + 0.004 = 0.006, ref now #2.
	if total, ref := readCost(t, st, "rp-a", "s-derived"); total != 0.006 || ref != "openai/gpt-4o#2" {
		t.Fatalf("re-priced cost = %v ref %q, want 0.006 / #2", total, ref)
	}
	// Provided cost is UNTOUCHED — it never carried a ref, so the scan never saw it.
	if total, ref := readCost(t, st, "rp-a", "s-provided"); total != 0.99 || ref != "" {
		t.Fatalf("provided span mutated: cost=%v ref=%q", total, ref)
	}

	// Idempotent: the span moved to #2, so a second run for #1 finds nothing (no-op).
	res2, err := r.Run(ctx, storage.RepriceFilter{SnapshotRefID: "openai/gpt-4o#1"})
	if err != nil {
		t.Fatalf("reprice run 2: %v", err)
	}
	if res2.Repriced != 0 {
		t.Fatalf("second run repriced %d, want 0 (idempotent)", res2.Repriced)
	}
	if total, ref := readCost(t, st, "rp-a", "s-derived"); total != 0.006 || ref != "openai/gpt-4o#2" {
		t.Fatalf("second run mutated cost: %v / %q", total, ref)
	}
}

// TestReprice_TenantIsolation is the prove-the-negative: a discount re-price scoped to one
// project re-derives ONLY that project's spans and provably cannot cross into another
// tenant. It also proves discount-change re-pricing (same ref id, changed cost) and its
// idempotency (the ref doesn't move, so the second pass compares cost and skips).
func TestReprice_TenantIsolation(t *testing.T) {
	st, ps, _ := pgSetup(t)
	ctx := context.Background()

	upsertV1(t, ps)
	// Two derived spans in project A, one in project B — all priced at v1 = 0.0075.
	seedDerivedSpan(t, st, ps, "rp-a", "a1")
	seedDerivedSpan(t, st, ps, "rp-a", "a2")
	seedDerivedSpan(t, st, ps, "rp-b", "b1")

	// Project A gets a 50% discount; re-price ONLY project A.
	if err := ps.SetDiscount(ctx, "rp-a", 0.5, "session:admin@x"); err != nil {
		t.Fatalf("set discount: %v", err)
	}
	r := newRunner(st, ps)
	res, err := r.Run(ctx, storage.RepriceFilter{ProjectID: "rp-a"})
	if err != nil {
		t.Fatalf("discount reprice: %v", err)
	}
	if !res.Complete || res.Repriced != 2 || res.Scanned != 2 {
		t.Fatalf("run result = %+v, want complete/repriced=2/scanned=2", res)
	}

	// Project A's spans are halved (0.0075 * 0.5 = 0.00375), ref UNCHANGED (#1).
	for _, id := range []string{"a1", "a2"} {
		if total, ref := readCost(t, st, "rp-a", id); total != 0.00375 || ref != "openai/gpt-4o#1" {
			t.Fatalf("A/%s = %v / %q, want 0.00375 / #1", id, total, ref)
		}
	}
	// PROVE-THE-NEGATIVE: project B is untouched — the scan's project_id predicate makes
	// crossing the boundary impossible.
	if total, ref := readCost(t, st, "rp-b", "b1"); total != 0.0075 || ref != "openai/gpt-4o#1" {
		t.Fatalf("tenant leak: B/b1 = %v / %q, want 0.0075 / #1 (unchanged)", total, ref)
	}

	// Idempotent even though the ref never moved: the second pass re-derives the same
	// discounted cost, compares equal, and skips (scanned=2, repriced=0).
	res2, err := r.Run(ctx, storage.RepriceFilter{ProjectID: "rp-a"})
	if err != nil {
		t.Fatalf("discount reprice 2: %v", err)
	}
	if res2.Scanned != 2 || res2.Repriced != 0 {
		t.Fatalf("second discount run = %+v, want scanned=2/repriced=0 (idempotent)", res2)
	}
}

// TestReprice_DroppedBucketConverges is the prove-the-negative for the per-leaf
// cost_details fold: a new price version that DROPS a bucket rate (v1 prices cache_read,
// v2 does not) must still keep sum(cost_details)==total_cost and CONVERGE (a second run is
// a no-op), because the fold has no leaf tombstone. The runner zeroes the dropped leaf.
func TestReprice_DroppedBucketConverges(t *testing.T) {
	st, ps, pool := pgSetup(t)
	ctx := context.Background()

	// v1 prices input, output, AND cache_read.
	if _, err := ps.Upsert(ctx, pricing.Entry{
		Provider: "openai", Model: "gpt-4o", EffectiveFrom: "2026-01-01T00:00:00Z",
		Rates: map[string]pricing.Rate{
			"input": {PerToken: 0.0000025}, "output": {PerToken: 0.00001},
			"cache_read": {PerToken: 0.0000005, Reduces: "input"},
		},
	}, "session:admin@x"); err != nil {
		t.Fatalf("upsert v1: %v", err)
	}
	// Seed a span WITH cache_read usage, priced at v1.
	p := map[string]any{
		"project_id": "rp-a", "id": "drop1", "trace_id": "t-drop1", "kind": "generation",
		"model": "gpt-4o", "provider": "openai", "start_time": "2026-06-01T12:00:00Z",
		"provided_usage_details": map[string]any{"input": 1000.0, "output": 500.0, "cache_read": 200.0},
	}
	discount, _, _ := ps.GetDiscount(ctx, "rp-a")
	if _, err := costderive.DeriveSpanCost(ctx, p, ps, discount, map[string]*pricing.Entry{}); err != nil {
		t.Fatalf("seed derive: %v", err)
	}
	if err := st.PersistSpan(ctx, storage.Event{Op: storage.OpUpsert, EventTS: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC), EventID: "drop1-ingest", Payload: p}); err != nil {
		t.Fatalf("seed persist: %v", err)
	}

	// v2 DROPS cache_read (and changes base rates).
	if _, err := ps.Upsert(ctx, pricing.Entry{
		Provider: "openai", Model: "gpt-4o", EffectiveFrom: "2026-01-01T00:00:00Z",
		Rates: map[string]pricing.Rate{"input": {PerToken: 0.000002}, "output": {PerToken: 0.000008}},
	}, "session:admin@x"); err != nil {
		t.Fatalf("upsert v2: %v", err)
	}

	r := newRunner(st, ps)
	if res, err := r.Run(ctx, storage.RepriceFilter{SnapshotRefID: "openai/gpt-4o#1"}); err != nil || res.Repriced != 1 {
		t.Fatalf("reprice: res=%+v err=%v", res, err)
	}

	// The stored breakdown must be consistent: sum(cost_details) == total_cost, and the
	// dropped bucket is present as 0 (the fold can't remove it), not stale at its v1 value.
	doc, _ := st.GetSpan(ctx, "rp-a", "drop1")
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cd, _ := m["cost_details"].(map[string]any)
	total, _ := m["total_cost"].(float64)
	var sum float64
	for _, v := range cd {
		if f, ok := v.(float64); ok {
			sum += f
		}
	}
	if cr, ok := cd["cache_read"].(float64); !ok || cr != 0.0 {
		t.Fatalf("dropped bucket cache_read = %v (ok=%v), want 0", cd["cache_read"], ok)
	}
	if sum != total {
		t.Fatalf("sum(cost_details)=%v != total_cost=%v (breakdown corrupt)", sum, total)
	}

	// CONVERGENCE: a second run over the same filter finds the span at #2 (no match) — but
	// to prove per-span convergence, re-run scoped to the project and assert repriced=0.
	_, _ = pool.Exec(ctx, `DELETE FROM reprice_state WHERE run_key LIKE '%openai/gpt-4o%' OR run_key LIKE '%rp-a%'`)
	if res, err := r.Run(ctx, storage.RepriceFilter{ProjectID: "rp-a"}); err != nil || res.Repriced != 0 {
		t.Fatalf("second run must be a no-op (converged): res=%+v err=%v", res, err)
	}
}

// TestReprice_EmptyFilterRefused guards the unbounded-scan footgun: a run with no selector
// must refuse rather than scan every derived span in the instance.
func TestReprice_EmptyFilterRefused(t *testing.T) {
	st, ps, _ := pgSetup(t)
	if _, err := newRunner(st, ps).Run(context.Background(), storage.RepriceFilter{}); err == nil {
		t.Fatal("empty filter must be refused")
	}
}
