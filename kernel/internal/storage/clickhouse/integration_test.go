package clickhouse

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/query"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// dialCH connects to a real ClickHouse from LLMOBS_CH_TEST_DSN, or skips. Kept
// env-gated (not build-tagged) so the test always compiles in CI; it runs only
// where a ClickHouse is provisioned (the scale e2e / a local `make ch-up`).
func dialCH(t *testing.T) driver.Conn {
	t.Helper()
	dsn := os.Getenv("LLMOBS_CH_TEST_DSN")
	if dsn == "" {
		t.Skip("LLMOBS_CH_TEST_DSN unset — skipping real-ClickHouse integration test")
	}
	opts, err := ch.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	conn, err := ch.Open(opts)
	if err != nil {
		t.Fatalf("open clickhouse: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		t.Fatalf("ping clickhouse: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// freshSchema migrates a clean standalone schema, dropping the tables first so
// re-runs are hermetic.
func freshSchema(t *testing.T, conn driver.Conn) {
	t.Helper()
	ctx := context.Background()
	for _, tbl := range []string{"spans", "scores", "erasure_suppression", "erasure_audit", "schema_migrations"} {
		if err := conn.Exec(ctx, "DROP TABLE IF EXISTS "+tbl); err != nil {
			t.Fatalf("drop %s: %v", tbl, err)
		}
	}
	if err := Migrate(ctx, conn, Config{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// R-CH2: a second Migrate over the same schema is a no-op (idempotent).
	if err := Migrate(ctx, conn, Config{}); err != nil {
		t.Fatalf("second migrate (idempotency): %v", err)
	}
}

func up(ts int, id string, payload map[string]any) storage.Event {
	p := map[string]any{"project_id": "p", "id": id}
	for k, v := range payload {
		p[k] = v
	}
	return storage.Event{
		Op:      storage.OpUpsert,
		EventTS: time.Unix(int64(ts), 0).UTC(),
		EventID: id + "-" + string(rune('0'+ts)),
		Payload: p,
	}
}

func TestIntegrationMigrateAndMerge(t *testing.T) {
	conn := dialCH(t)
	freshSchema(t, conn)
	ctx := context.Background()
	s := NewStore(conn)

	// Two out-of-order events for the same (project_id, id): merge-on-write must
	// converge to the union, exactly as lite folds — proving the shared fold drives
	// the real write path.
	start := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.PersistSpan(ctx, up(2, "s1", map[string]any{
		"kind": "generation", "start_time": start, "name": "second",
		"attributes": map[string]any{"y": 2.0},
	})); err != nil {
		t.Fatalf("persist span (ts2): %v", err)
	}
	if err := s.PersistSpan(ctx, up(1, "s1", map[string]any{
		"kind": "generation", "start_time": start, "name": "first",
		"attributes": map[string]any{"x": 1.0},
	})); err != nil {
		t.Fatalf("persist span (ts1): %v", err)
	}

	state, _, err := s.readSettled(ctx, "spans", "p", "s1")
	if err != nil {
		t.Fatalf("read settled: %v", err)
	}
	// name is last-writer-wins by event_ts: ts2 ("second") wins over the later-
	// arriving-but-older ts1 ("first").
	if got := state["name"]; got != "second" {
		t.Errorf("name = %v, want \"second\" (higher event_ts wins)", got)
	}
	// attributes deep-merge across both events.
	attrs, _ := state["attributes"].(map[string]any)
	if attrs["x"] != 1.0 || attrs["y"] != 2.0 {
		t.Errorf("attributes = %v, want both x=1 and y=2 (deep merge)", attrs)
	}
}

func TestIntegrationPathologicalQueryCapped(t *testing.T) {
	conn := dialCH(t)
	freshSchema(t, conn)
	ctx := context.Background()
	s := NewStore(conn)

	// Seed a handful of spans.
	start := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 0; i < 20; i++ {
		id := "sp" + string(rune('a'+i))
		if err := s.PersistSpan(ctx, up(1, id, map[string]any{"kind": "generation", "start_time": start})); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// A pathological cap: at most 1 row may be scanned. The query MUST be rejected
	// by ClickHouse (capped), not run to completion — and the connection must stay
	// usable afterward (a cap protects the cluster; it does not take it down).
	s.SetReadLimits(ReadLimits{
		MaxExecutionTime: 30 * time.Second,
		MaxMemoryUsage:   1 << 30,
		MaxRowsToRead:    1,
		MaxBytesToRead:   1 << 30,
	})
	_, err := s.QuerySpans(ctx, "project_id = ?", []any{"p"}, "", 1000)
	if err == nil {
		t.Fatal("expected the row-cap to reject the pathological scan, got nil error")
	}
	if !strings.Contains(err.Error(), "rows") && !strings.Contains(err.Error(), "limit") && !strings.Contains(strings.ToLower(err.Error()), "exceed") {
		t.Fatalf("expected a rows-limit error, got: %v", err)
	}

	// The cluster survived: a sane-limit query on the same connection works.
	s.SetReadLimits(ReadLimits{
		MaxExecutionTime: 30 * time.Second,
		MaxMemoryUsage:   1 << 30,
		MaxRowsToRead:    10_000_000,
		MaxBytesToRead:   1 << 30,
	})
	rows, err := s.QuerySpans(ctx, "project_id = ?", []any{"p"}, "", 1000)
	if err != nil {
		t.Fatalf("post-cap query failed (connection poisoned?): %v", err)
	}
	if len(rows) != 20 {
		t.Fatalf("post-cap query returned %d rows, want 20", len(rows))
	}
}

func TestIntegrationEraseSpans(t *testing.T) {
	conn := dialCH(t)
	freshSchema(t, conn)
	ctx := context.Background()
	// Erasure resolves ids through readGuard (apply_deleted_mask), so it is fail-closed on
	// the resource caps exactly like every other read — configure the store as production
	// does (buildScaleStore always sets ReadLimits + the lazy-materialization guard).
	s := readyStore(t, conn)
	start := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	// u1 has two spans; one is later soft-deleted (its payload must STILL be erased).
	mk := func(id, user string, del bool) storage.Event {
		f := map[string]any{"user_id": user, "kind": "generation", "start_time": start.Format(time.RFC3339Nano)}
		if del {
			f["is_deleted"] = true
		}
		return up(1, id, f)
	}
	for _, ev := range []storage.Event{mk("e1", "u1", false), mk("e2", "u1", true), mk("e3", "u2", false)} {
		if err := s.PersistSpan(ctx, ev); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	count, auditID, err := s.EraseSpans(ctx, "p", "u1", "admin@x", start.Add(-time.Hour), start.Add(time.Hour))
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	// Both u1 spans erased — including the soft-deleted e2 (payload removal).
	if count != 2 {
		t.Fatalf("erased count = %d, want 2 (incl. the soft-deleted span)", count)
	}
	if auditID == "" {
		t.Fatal("empty audit id")
	}

	// e1/e2 physically gone; u2's e3 untouched.
	var remaining uint64
	row := conn.QueryRow(ctx, "SELECT count() FROM spans WHERE project_id='p' AND id IN ('e1','e2')")
	if err := row.Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("erased spans still present: %d rows", remaining)
	}
	// Suppression tombstones written for both erased ids (G3, before the delete).
	var supp uint64
	if err := conn.QueryRow(ctx, "SELECT count() FROM erasure_suppression WHERE project_id='p' AND id IN ('e1','e2')").Scan(&supp); err != nil {
		t.Fatalf("count suppression: %v", err)
	}
	if supp != 2 {
		t.Fatalf("suppression tombstones = %d, want 2", supp)
	}
	// Audit row recorded.
	var audits uint64
	if err := conn.QueryRow(ctx, "SELECT count() FROM erasure_audit WHERE id = ?", auditID).Scan(&audits); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if audits != 1 {
		t.Fatalf("audit rows = %d, want 1", audits)
	}
}

// readyStore builds a Store configured exactly like buildScaleStore's read path:
// resource caps set + the erasure read-back guards active (apply_deleted_mask via
// readGuard, plus the lazy-materialization guard when the server has that setting).
func readyStore(t *testing.T, conn driver.Conn) *Store {
	t.Helper()
	info, err := ProbeServer(context.Background(), conn)
	if err != nil {
		t.Fatalf("probe server: %v", err)
	}
	s := NewStore(conn)
	s.SetReadLimits(ReadLimits{
		MaxExecutionTime: 30 * time.Second,
		MaxMemoryUsage:   1 << 30,
		MaxRowsToRead:    10_000_000,
		MaxBytesToRead:   1 << 30,
	})
	s.SetLazyMaterializationGuard(info.HasLazyMaterialization)
	return s
}

func compileCH(t *testing.T, target string) *query.Compiled {
	t.Helper()
	start := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	doc := map[string]any{
		"target": target,
		"timeRange": map[string]any{
			"from": start.Add(-time.Hour).Format(time.RFC3339),
			"to":   start.Add(time.Hour).Format(time.RFC3339),
		},
	}
	var c *query.Compiled
	var err error
	switch target {
	case "spans":
		c, err = query.CompileSpansForDialect(doc, "p", 365*24*time.Hour, query.ClickHouseDialect)
	case "traces":
		c, err = query.CompileTracesForDialect(doc, "p", 365*24*time.Hour, query.ClickHouseDialect)
	}
	if err != nil {
		t.Fatalf("compile %s: %v", target, err)
	}
	return c
}

func assertNoErasedSpan(t *testing.T, label string, rows []json.RawMessage) {
	t.Helper()
	for _, r := range rows {
		var m map[string]any
		if err := json.Unmarshal(r, &m); err != nil {
			t.Fatalf("%s: undecodable row: %v", label, err)
		}
		if id, _ := m["id"].(string); id == "e1" || id == "e2" {
			t.Fatalf("%s: erased span %s returned — GDPR read-back", label, id)
		}
		if s := string(r); strings.Contains(s, "SECRET-e1") || strings.Contains(s, "SECRET-e2") {
			t.Fatalf("%s: erased payload leaked: %s", label, s)
		}
	}
}

// TestIntegrationErasedSpanNotReadableEveryPath is the #88 prove-the-negative — the
// arc's highest-stakes proof. After a GDPR erase, a lightweight-deleted span must be
// unreadable through EVERY read path, and — critically — WITHOUT an OPTIMIZE FINAL: the
// deleted rows are still physically present, only masked (the lazy/unmaterialized window
// a plan reorder could otherwise resurrect from). apply_deleted_mask=1 + the
// lazy-materialization guard must exclude the erased span at every path, and no erased
// payload may leak. Proven again AFTER the physical merge, so the guarantee holds in both
// states.
func TestIntegrationErasedSpanNotReadableEveryPath(t *testing.T) {
	conn := dialCH(t)
	freshSchema(t, conn)
	ctx := context.Background()
	s := readyStore(t, conn)
	start := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	// u1: two spans in trace t1 (one later soft-deleted — its payload must STILL be
	// erased); u2: one span in trace t2, which must remain fully readable throughout.
	span := func(id, user, trace string, del bool) storage.Event {
		f := map[string]any{
			"user_id": user, "trace_id": trace, "kind": "generation", "name": "gen",
			"start_time": start.Format(time.RFC3339Nano),
			"end_time":   start.Add(time.Second).Format(time.RFC3339Nano),
			"input":      "SECRET-" + id + "-prompt", "output": "SECRET-" + id + "-completion",
		}
		if del {
			f["is_deleted"] = true
		}
		return up(1, id, f)
	}
	for _, ev := range []storage.Event{
		span("e1", "u1", "t1", false),
		span("e2", "u1", "t1", true),
		span("e3", "u2", "t2", false),
	} {
		if err := s.PersistSpan(ctx, ev); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	count, _, err := s.EraseSpans(ctx, "p", "u1", "admin@x", start.Add(-time.Hour), start.Add(time.Hour))
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	if count != 2 {
		t.Fatalf("erased %d, want 2 (incl. the soft-deleted span)", count)
	}

	assertUnreadable := func(phase string) {
		// 1) Point read: erased ids gone, non-erased still present.
		for _, id := range []string{"e1", "e2"} {
			doc, err := s.GetSpan(ctx, "p", id)
			if err != nil {
				t.Fatalf("%s GetSpan(%s): %v", phase, id, err)
			}
			if doc != nil {
				t.Fatalf("%s: erased span %s readable via GetSpan — GDPR read-back: %s", phase, id, doc)
			}
		}
		if doc, _ := s.GetSpan(ctx, "p", "e3"); doc == nil {
			t.Fatalf("%s: non-erased span e3 must remain readable", phase)
		}
		// 2) Compiled list read.
		cs := compileCH(t, "spans")
		spans, err := s.QuerySpans(ctx, cs.Where, cs.Args, cs.Order, cs.Limit)
		if err != nil {
			t.Fatalf("%s QuerySpans: %v", phase, err)
		}
		assertNoErasedSpan(t, phase+" QuerySpans", spans)
		// 3) Trace-tree read: the wholly-erased trace t1 returns nothing.
		t1, err := s.GetTraceSpans(ctx, "p", "t1")
		if err != nil {
			t.Fatalf("%s GetTraceSpans(t1): %v", phase, err)
		}
		if len(t1) != 0 {
			t.Fatalf("%s: erased trace t1 still returns %d spans", phase, len(t1))
		}
		// 4) Trace projection: t1 must not appear; t2 must.
		ct := compileCH(t, "traces")
		traces, err := s.QueryTraces(ctx, ct.Where, ct.Args, ct.Order, ct.Limit)
		if err != nil {
			t.Fatalf("%s QueryTraces: %v", phase, err)
		}
		sawT2 := false
		for _, r := range traces {
			var m map[string]any
			_ = json.Unmarshal(r, &m)
			if id, _ := m["id"].(string); id == "t1" {
				t.Fatalf("%s: trace t1 (all spans erased) still returned", phase)
			} else if id == "t2" {
				sawT2 = true
			}
		}
		if !sawT2 {
			t.Fatalf("%s: non-erased trace t2 must remain readable", phase)
		}
	}

	// The load-bearing case: BEFORE any physical merge (masked-only, lazy window).
	assertUnreadable("unmerged")
	// And after forcing the merge — the guarantee holds in both states.
	if err := conn.Exec(ctx, "OPTIMIZE TABLE spans FINAL"); err != nil {
		t.Fatalf("optimize: %v", err)
	}
	assertUnreadable("merged")
}

// TestIntegrationDeletedMaskIsLoadBearing proves the #88 fix is load-bearing, not
// incidental: after a lightweight-delete erase and BEFORE any merge, the erased row is
// still PHYSICALLY present, and only the deleted mask hides it. Reading with
// apply_deleted_mask=0 (the misconfig/bug our readGuard defends against) returns the
// erased row; reading with apply_deleted_mask=1 (what readGuard pins on every read) does
// not. So the pinned setting is exactly what stands between a GDPR-erased span and a
// read-back — remove it and the row comes back.
func TestIntegrationDeletedMaskIsLoadBearing(t *testing.T) {
	conn := dialCH(t)
	freshSchema(t, conn)
	ctx := context.Background()
	s := readyStore(t, conn)
	start := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	ev := up(1, "mask1", map[string]any{
		"user_id": "u1", "kind": "generation", "name": "gen",
		"start_time": start.Format(time.RFC3339Nano),
		"input":      "SECRET-mask1-prompt",
	})
	if err := s.PersistSpan(ctx, ev); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := s.EraseSpans(ctx, "p", "u1", "admin@x", start.Add(-time.Hour), start.Add(time.Hour)); err != nil {
		t.Fatalf("erase: %v", err)
	}
	// DELIBERATELY do not OPTIMIZE FINAL — the row is masked, not yet physically merged.

	countWithMask := func(mask int) uint64 {
		var n uint64
		q := "SELECT count() FROM spans WHERE project_id='p' AND id='mask1' SETTINGS apply_deleted_mask=" +
			itoaMask(mask)
		if err := conn.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatalf("count (mask=%d): %v", mask, err)
		}
		return n
	}
	// Without the mask, the erased row is still physically there — the read-back the bug exposes.
	if got := countWithMask(0); got == 0 {
		t.Skip("row already physically merged away before assertion; mask-off read-back not demonstrable on this run")
	}
	// With the mask (what readGuard pins), the erased row is excluded.
	if got := countWithMask(1); got != 0 {
		t.Fatalf("apply_deleted_mask=1 must exclude the erased row, still saw %d — the pinned guard is not effective", got)
	}
}

func itoaMask(i int) string {
	if i == 0 {
		return "0"
	}
	return "1"
}

// TestIntegrationSuppressedIdNotReadableEvenIfReinserted is the DETERMINISTIC #77
// read-side proof — it reproduces the concurrent-race OUTCOME directly, with no timing
// dependence: a normal, non-lightweight-deleted span exists for an id that carries an
// unexpired suppression tombstone (exactly what a backfill/re-ingest that lost the write
// race to a concurrent erase leaves behind — the row lands, then the erase's tombstone
// commits). apply_deleted_mask does NOT hide this row (it was never lightweight-deleted);
// only the read-side suppression exclusion keeps it out of every read. This proves the
// concurrent no-resurrection guarantee by CONSTRUCTION, not by a lucky interleaving.
func TestIntegrationSuppressedIdNotReadableEvenIfReinserted(t *testing.T) {
	conn := dialCH(t)
	freshSchema(t, conn)
	ctx := context.Background()
	s := readyStore(t, conn)
	start := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	// A normal, non-deleted span lands (as a racing backfill/re-ingest would insert it).
	ev := up(1, "race1", map[string]any{
		"user_id": "u1", "trace_id": "tr", "kind": "generation", "name": "gen",
		"start_time": start.Format(time.RFC3339Nano), "input": "SECRET-race1-prompt",
	})
	if err := s.PersistSpan(ctx, ev); err != nil {
		t.Fatalf("persist: %v", err)
	}
	// A second, NON-suppressed span so aggregations have a clear expected count of 1.
	keep := up(1, "keep1", map[string]any{
		"user_id": "u2", "trace_id": "tr2", "kind": "generation", "name": "gen",
		"start_time": start.Format(time.RFC3339Nano),
	})
	if err := s.PersistSpan(ctx, keep); err != nil {
		t.Fatalf("persist keep: %v", err)
	}
	// Then a suppression tombstone appears for that id — the race outcome: the erase's
	// tombstone commits AFTER the row landed, so the row is NOT lightweight-deleted.
	if err := conn.Exec(ctx,
		"INSERT INTO erasure_suppression (project_id, id, audit_id, expires_at, event_ts) VALUES (?,?,?,?,?)",
		"p", "race1", "aud-race", time.Now().UTC().Add(time.Hour), time.Now().UTC()); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	// Precondition: the row is physically present and NOT lightweight-deleted — so the
	// deleted mask cannot be what hides it; only the suppression exclusion can.
	var present uint64
	if err := conn.QueryRow(ctx,
		"SELECT count() FROM spans WHERE project_id='p' AND id='race1' SETTINGS apply_deleted_mask=1").Scan(&present); err != nil {
		t.Fatalf("precondition count: %v", err)
	}
	if present == 0 {
		t.Fatal("precondition: the reinserted row must be physically present (mask does not hide a non-lightweight-deleted row)")
	}

	// Every read must now exclude race1 — the resurrection is never returned.
	if doc, _ := s.GetSpan(ctx, "p", "race1"); doc != nil {
		t.Fatalf("GetSpan returned a suppressed-but-reinserted span — resurrection: %s", doc)
	}
	cs := compileCH(t, "spans")
	spans, err := s.QuerySpans(ctx, cs.Where, cs.Args, cs.Order, cs.Limit)
	if err != nil {
		t.Fatalf("QuerySpans: %v", err)
	}
	for _, r := range spans {
		if strings.Contains(string(r), "race1") || strings.Contains(string(r), "SECRET-race1") {
			t.Fatalf("QuerySpans returned suppressed-but-reinserted span: %s", r)
		}
	}
	tspans, err := s.GetTraceSpans(ctx, "p", "tr")
	if err != nil {
		t.Fatalf("GetTraceSpans: %v", err)
	}
	if len(tspans) != 0 {
		t.Fatalf("GetTraceSpans returned %d spans for a fully-suppressed trace — resurrection", len(tspans))
	}
	ct := compileCH(t, "traces")
	traces, err := s.QueryTraces(ctx, ct.Where, ct.Args, ct.Order, ct.Limit)
	if err != nil {
		t.Fatalf("QueryTraces: %v", err)
	}
	for _, r := range traces {
		var m map[string]any
		_ = json.Unmarshal(r, &m)
		if id, _ := m["id"].(string); id == "tr" {
			t.Fatalf("QueryTraces returned trace of a fully-suppressed span — resurrection")
		}
	}
	// 5) Aggregation over spans must NOT count the resurrected span (the fifth read site).
	aggDoc := map[string]any{
		"target": "spans",
		"timeRange": map[string]any{
			"from": start.Add(-time.Hour).Format(time.RFC3339),
			"to":   start.Add(time.Hour).Format(time.RFC3339),
		},
		"aggregations": []any{map[string]any{"op": "count", "as": "n"}},
	}
	ca, err := query.CompileAggregationForDialect(aggDoc, "p", 365*24*time.Hour, "spans", query.ClickHouseDialect)
	if err != nil {
		t.Fatalf("compile agg: %v", err)
	}
	groups, err := s.QueryAggregation(ctx, "spans", ca.Select, ca.Where, ca.GroupBy, ca.Args)
	if err != nil {
		t.Fatalf("QueryAggregation: %v", err)
	}
	if len(ca.AggMetas) == 0 {
		t.Fatal("compiled aggregation has no agg metas")
	}
	alias := ca.AggMetas[0].Alias
	// Only keep1 is countable; the suppressed-but-reinserted race1 must be excluded.
	var total int64
	for _, g := range groups {
		switch v := g[alias].(type) {
		case uint64:
			total += int64(v)
		case int64:
			total += v
		case float64:
			total += int64(v)
		}
	}
	if total != 1 {
		t.Fatalf("aggregation counted %d spans (alias %q, groups=%v), want 1 — a resurrected span leaked into an aggregate", total, alias, groups)
	}
}

func TestIntegrationPreflightPasses(t *testing.T) {
	conn := dialCH(t)
	// The test user (DEFAULT_ACCESS_MANAGEMENT) holds the full grant set, so the
	// functional preflight must pass cleanly against a real node.
	if err := Preflight(context.Background(), conn, "llmobs"); err != nil {
		t.Fatalf("preflight against fully-granted user failed: %v", err)
	}
}

func TestIntegrationErasureSuppression(t *testing.T) {
	conn := dialCH(t)
	freshSchema(t, conn)
	ctx := context.Background()
	s := NewStore(conn)

	// Write an unexpired tombstone for (p, s2), then a re-delivery must be
	// suppressed (G3) — not resurrected.
	if err := conn.Exec(ctx,
		"INSERT INTO erasure_suppression (project_id, id, audit_id, expires_at, event_ts) VALUES (?, ?, ?, ?, ?)",
		"p", "s2", "aud1", time.Now().UTC().Add(time.Hour), time.Now().UTC()); err != nil {
		t.Fatalf("insert tombstone: %v", err)
	}
	// Forgery case (the security-review regression guard): a re-delivered erased
	// span carrying a FUTURE event timestamp must STILL be suppressed. Expiry is
	// evaluated against server time, never the attacker-controlled event ts — else
	// a forged 2099 timestamp would skip suppression and resurrect erased data.
	forged := up(1, "s2", map[string]any{
		"kind": "generation", "start_time": time.Now().UTC().Format(time.RFC3339Nano), "name": "resurrected?",
	})
	forged.EventTS = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC) // far past the 1h tombstone expiry
	err := s.PersistSpan(ctx, forged)
	if !errors.Is(err, storage.ErrSuppressedByErasure) {
		t.Fatalf("expected ErrSuppressedByErasure for future-dated forged event, got %v", err)
	}
	// And nothing was written.
	state, _, rerr := s.readSettled(ctx, "spans", "p", "s2")
	if rerr != nil {
		t.Fatalf("read settled: %v", rerr)
	}
	if len(state) != 0 {
		t.Errorf("suppressed span must not be stored, got %v", state)
	}
}
