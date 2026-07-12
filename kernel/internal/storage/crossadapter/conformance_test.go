// Package crossadapter holds the SQL-level cross-adapter conformance proof: it
// drives identical event streams and identical DSL queries through BOTH real
// storage engines (Postgres-lite and ClickHouse-scale) and asserts byte-identical
// Query-API output. This makes real the governing rule that the ClickHouse adapter
// MUST produce identical observable Query-API results to Postgres — fold-level
// conformance (tools/conformance) proves the merge is identical; THIS proves the
// two SQL dialects + planners produce the same observable answer.
//
// Env-gated on BOTH LLMOBS_TEST_DATABASE_URL (Postgres) and LLMOBS_CH_TEST_DSN
// (ClickHouse); skips unless both are present. Compiles always.
//
// Run the DB-backed integration suites serially (`go test -p 1 ./...`): this
// package and internal/storage/clickhouse both reset (DROP) the shared ClickHouse
// tables, so parallel package execution against one instance would clobber them.
//
// Design note — this harness is a SECURITY mechanism, not only an elegance one.
// A safety property proven on ONE adapter is not proven until proven on EVERY
// adapter: a HIGH-severity SQL injection bug was invisible on Postgres (a dialect difference
// ClickHouse does not forgive), so any suite that ran against Postgres alone — or a
// mocked ClickHouse — would have shipped it to every scale deployment. Because the
// two engines diverge in ways no single-engine test can see (NULL vs ” sentinel,
// percentile interpolation, boolean encoding, literal escaping), every parity or
// safety case a reviewer finds becomes a PERMANENT fixture here (percentile,
// boolean group-by, unset-string counts below; the injection case in the query
// package's dialect_injection_test.go). The proof only strengthens — it never
// regresses.
package crossadapter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/query"
	"github.com/m7mdhka/llmobs/kernel/internal/storage"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/clickhouse"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
)

const projectID = "p-cross"

var maxWindow = 365 * 24 * time.Hour

// harness holds both live adapters, seeded identically.
type harness struct {
	pg *postgres.Store
	ch *clickhouse.Store
}

func setup(t *testing.T) *harness {
	t.Helper()
	pgURL := os.Getenv("LLMOBS_TEST_DATABASE_URL")
	chDSN := os.Getenv("LLMOBS_CH_TEST_DSN")
	if pgURL == "" || chDSN == "" {
		t.Skip("set BOTH LLMOBS_TEST_DATABASE_URL and LLMOBS_CH_TEST_DSN to run the cross-adapter conformance")
	}
	ctx := context.Background()

	// --- Postgres ---
	pool, err := pgxpool.New(ctx, pgURL)
	if err != nil {
		t.Fatalf("pg connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("pg migrate: %v", err)
	}
	// Clean slate + the org/project rows scores' FK needs.
	for _, stmt := range []string{
		`DELETE FROM scores WHERE project_id = $1`,
		`DELETE FROM spans WHERE project_id = $1`,
	} {
		if _, err := pool.Exec(ctx, stmt, projectID); err != nil {
			t.Fatalf("pg clean: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name) VALUES ('o-cross','x') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("pg org: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO projects (id, org_id, name) VALUES ($1,'o-cross','x') ON CONFLICT DO NOTHING`, projectID); err != nil {
		t.Fatalf("pg project: %v", err)
	}
	pgStore := postgres.NewStore(pool)

	// --- ClickHouse ---
	opts, err := ch.ParseDSN(chDSN)
	if err != nil {
		t.Fatalf("ch dsn: %v", err)
	}
	conn, err := ch.Open(opts)
	if err != nil {
		t.Fatalf("ch open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, tbl := range []string{"spans", "scores", "erasure_suppression", "erasure_audit", "schema_migrations"} {
		if err := conn.Exec(ctx, "DROP TABLE IF EXISTS "+tbl); err != nil {
			t.Fatalf("ch drop %s: %v", tbl, err)
		}
	}
	if err := clickhouse.Migrate(ctx, conn, clickhouse.Config{}); err != nil {
		t.Fatalf("ch migrate: %v", err)
	}
	chStore := clickhouse.NewStore(conn)
	chStore.SetReadLimits(clickhouse.ReadLimits{
		MaxExecutionTime: 30 * time.Second,
		MaxMemoryUsage:   2 << 30,
		MaxRowsToRead:    50_000_000,
		MaxBytesToRead:   5 << 30,
	})

	h := &harness{pg: pgStore, ch: chStore}
	h.seed(t, conn)
	return h
}

// seed writes the same span/score events into both adapters.
func (h *harness) seed(t *testing.T, conn chdriver.Conn) {
	t.Helper()
	ctx := context.Background()
	for _, ev := range seedEvents() {
		if err := h.pg.PersistSpan(ctx, ev); err != nil {
			t.Fatalf("pg persist span %v: %v", ev.Payload["id"], err)
		}
		if err := h.ch.PersistSpan(ctx, ev); err != nil {
			t.Fatalf("ch persist span %v: %v", ev.Payload["id"], err)
		}
	}
	for _, ev := range seedScores() {
		if err := h.pg.PersistScore(ctx, ev); err != nil {
			t.Fatalf("pg persist score: %v", err)
		}
		if err := h.ch.PersistScore(ctx, ev); err != nil {
			t.Fatalf("ch persist score: %v", err)
		}
	}
	// Force the ClickHouse settled rows to merge so read-time dedup and physical
	// collapse are both exercised (not just the read-time LIMIT 1 BY path).
	_ = conn.Exec(ctx, "OPTIMIZE TABLE spans FINAL")
	_ = conn.Exec(ctx, "OPTIMIZE TABLE scores FINAL")
}

func ts(sec int) time.Time { return time.Date(2026, 6, 1, 12, 0, sec, 0, time.UTC) }

func spanEv(id string, eventTS int, fields map[string]any) storage.Event {
	p := map[string]any{"project_id": projectID, "id": id}
	for k, v := range fields {
		p[k] = v
	}
	return storage.Event{Op: storage.OpUpsert, EventTS: ts(eventTS), EventID: id + "-" + itoa(eventTS), Payload: p}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// seedEvents builds a span set exercising: set/unset dimensions (NULL vs ”),
// attributes (string + numeric maps, present/absent/non-number), an error status,
// two traces (one complete, one with an orphan-parent-ref → incomplete_trace),
// and out-of-order merge.
func seedEvents() []storage.Event {
	base := func(id, trace, parent, name string, start int, extra map[string]any) storage.Event {
		f := map[string]any{
			"trace_id": trace, "parent_span_id": parent, "kind": "generation", "name": name,
			"start_time": ts(start).Format(time.RFC3339Nano),
			"end_time":   ts(start + 1).Format(time.RFC3339Nano),
		}
		for k, v := range extra {
			f[k] = v
		}
		return spanEv(id, start, f)
	}
	return []storage.Event{
		// trace t1: complete tree (root + child whose parent exists).
		base("s1", "t1", "", "root", 1, map[string]any{
			"user_id": "u1", "model": "gpt-4", "environment": "prod",
			"attributes":    map[string]any{"lang": "en", "score": 0.9},
			"usage_details": map[string]any{"input": 10.0, "output": 5.0},
			"total_cost":    0.02,
		}),
		base("s2", "t1", "s1", "child", 2, map[string]any{
			"user_id": "u1", "model": "gpt-4",
			"attributes": map[string]any{"lang": "fr"},
			"status":     map[string]any{"code": "error"},
		}),
		// trace t2: an orphan whose parent is absent from the trace → incomplete_trace.
		base("s3", "t2", "missing-parent", "orphan", 3, map[string]any{
			"user_id": "u2", "environment": "dev",
			"usage_details": map[string]any{"input": 100.0},
		}),
		// A later out-of-order event for s1 adding a field; must converge identically.
		spanEv("s1", 5, map[string]any{"session_id": "sess-1"}),
	}
}

func seedScores() []storage.Event {
	sc := func(id, subjectID, name, dataType string, ts0 int, extra map[string]any) storage.Event {
		f := map[string]any{
			"subject_type": "trace", "subject_id": subjectID, "name": name, "data_type": dataType,
			"environment": "default", "timestamp": ts(ts0).Format(time.RFC3339Nano),
		}
		for k, v := range extra {
			f[k] = v
		}
		return spanEv(id, ts0, f)
	}
	return []storage.Event{
		sc("sc1", "t1", "quality", "numeric", 1, map[string]any{"value_numeric": 0.8, "source": "eval"}),
		sc("sc2", "t2", "quality", "numeric", 2, map[string]any{"value_numeric": 0.3, "source": "eval"}),
		sc("sc3", "t1", "verdict", "categorical", 3, map[string]any{"value_string": "good"}),
	}
}

// ---- the matrix ----

func TestCrossAdapterRowQueries(t *testing.T) {
	h := setup(t)
	cases := []struct {
		name   string
		target string
		doc    map[string]any
	}{
		{"spans all", "spans", qdoc("spans", nil, nil)},
		{"eq set dim", "spans", qdoc("spans", []any{cond("user_id", "eq", "u1")}, nil)},
		{"neq matches unset", "spans", qdoc("spans", []any{cond("user_id", "neq", "u1")}, nil)},
		{"is_null unset dim", "spans", qdoc("spans", []any{condOp("session_id", "is_null")}, nil)},
		{"in list", "spans", qdoc("spans", []any{cond("user_id", "in", []any{"u1", "u2"})}, nil)},
		{"not_in matches unset", "spans", qdoc("spans", []any{cond("user_id", "not_in", []any{"u1"})}, nil)},
		{"map string eq", "spans", qdoc("spans", []any{mapCond("attributes", "lang", "eq", "en")}, nil)},
		{"map string neq unset", "spans", qdoc("spans", []any{mapCond("attributes", "lang", "neq", "en")}, nil)},
		{"map exists", "spans", qdoc("spans", []any{mapCondOp("attributes", "lang", "exists")}, nil)},
		{"map numeric gt", "spans", qdoc("spans", []any{mapCondNum("usage_details", "input", "gt", 50)}, nil)},
		{"map numeric neq unset-matches", "spans", qdoc("spans", []any{mapCondNum("usage_details", "output", "neq", 999)}, nil)},
		{"computed duration gt", "spans", qdoc("spans", []any{cond("duration", "gt", 0.5)}, nil)},
		{"scores numeric gt", "scores", qdoc("scores", []any{cond("value_numeric", "gt", 0.5)}, nil)},
		{"traces all", "traces", qdoc("traces", nil, nil)},
		{"traces incomplete only", "traces", qdoc("traces", []any{condBool("incomplete_trace", true)}, nil)},
		{"traces error status", "traces", qdoc("traces", []any{cond("status.code", "eq", "error")}, nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pgRows := runPG(t, h, tc.target, tc.doc)
			chRows := runCH(t, h, tc.target, tc.doc)
			assertSameDocs(t, pgRows, chRows)
		})
	}
}

// TestCrossAdapterResponseBudget is the response-budget cross-adapter parity proof: the
// serialized-response byte ceiling must hold IDENTICALLY on Postgres (lite) and
// ClickHouse (scale) — the bug is BOTH-profile, so a fix proven on one engine is not
// proven. Against a tight budget BOTH adapters must refuse the SAME query with
// storage.ErrResponseTooLarge (the typed 413 the server maps, never an OOM/500);
// against a generous budget BOTH must return the SAME rows (the bound never corrupts a
// legitimate read). Because both adapters enforce the ONE shared storage.ResponseBudget
// in their scan loops, this asserts that single mechanism behaves the same through two
// different SQL engines + drivers.
func TestCrossAdapterResponseBudget(t *testing.T) {
	h := setup(t)
	doc := qdoc("spans", nil, nil) // spans-all: the seeded set has multiple non-trivial rows
	cpg := compile(t, "spans", doc, query.PostgresDialect)
	cch := compile(t, "spans", doc, query.ClickHouseDialect)

	// Sanity: an unbounded read returns a non-empty, cross-adapter-identical set.
	base := context.Background()
	pgFull, err := h.pg.QuerySpans(base, cpg.Where, cpg.Args, cpg.Order, cpg.Limit)
	if err != nil {
		t.Fatalf("pg unbounded: %v", err)
	}
	chFull, err := h.ch.QuerySpans(base, cch.Where, cch.Args, cch.Order, cch.Limit)
	if err != nil {
		t.Fatalf("ch unbounded: %v", err)
	}
	if len(pgFull) == 0 {
		t.Fatal("seed produced no spans — the budget parity proof needs rows")
	}
	assertSameDocs(t, pgFull, chFull)

	// Tight budget (1 byte): every real row exceeds it, so BOTH engines must refuse with
	// the SAME typed sentinel — not partial rows, not an engine-specific error, not an OOM.
	tight := storage.WithResponseBudget(context.Background(), 1)
	if _, err := h.pg.QuerySpans(tight, cpg.Where, cpg.Args, cpg.Order, cpg.Limit); !errors.Is(err, storage.ErrResponseTooLarge) {
		t.Fatalf("postgres: tight budget must return ErrResponseTooLarge, got %v", err)
	}
	if _, err := h.ch.QuerySpans(tight, cch.Where, cch.Args, cch.Order, cch.Limit); !errors.Is(err, storage.ErrResponseTooLarge) {
		t.Fatalf("clickhouse: tight budget must return ErrResponseTooLarge, got %v", err)
	}

	// Generous budget (32 MiB): the bound is inert for a normal page — BOTH return the
	// full, identical set (prove-the-negative: the guard does not degrade legitimate reads).
	roomy := storage.WithResponseBudget(context.Background(), 32<<20)
	pgOK, err := h.pg.QuerySpans(roomy, cpg.Where, cpg.Args, cpg.Order, cpg.Limit)
	if err != nil {
		t.Fatalf("postgres: generous budget must not refuse a normal page, got %v", err)
	}
	chOK, err := h.ch.QuerySpans(roomy, cch.Where, cch.Args, cch.Order, cch.Limit)
	if err != nil {
		t.Fatalf("clickhouse: generous budget must not refuse a normal page, got %v", err)
	}
	assertSameDocs(t, pgOK, chOK)
	assertSameDocs(t, pgFull, pgOK) // a roomy bound is byte-identical to no bound
}

func TestCrossAdapterAggregation(t *testing.T) {
	h := setup(t)
	cases := []struct {
		name   string
		target string
		doc    map[string]any
	}{
		{"count all", "spans", aggDoc("spans", nil, []any{agg("count", "", "")})},
		{"count group by user", "spans", aggDoc("spans", []any{"user_id"}, []any{agg("count", "", "")})},
		{"count group by unset dim", "spans", aggDoc("spans", []any{"session_id"}, []any{agg("count", "", "")})},
		{"sum total_cost", "spans", aggDoc("spans", nil, []any{agg("sum", "total_cost", "")})},
		{"avg map input", "spans", aggDoc("spans", nil, []any{agg("avg", "usage_details", "input")})},
		{"count_distinct user", "spans", aggDoc("spans", nil, []any{agg("count_distinct", "user_id", "")})},
		{"min/max map", "spans", aggDoc("spans", []any{"kind"}, []any{agg("min", "usage_details", "input"), agg("max", "usage_details", "input")})},
		{"scores avg by name", "scores", aggDoc("scores", []any{"name"}, []any{agg("avg", "value_numeric", "")})},
		{"p50 value_numeric", "scores", aggDoc("scores", nil, []any{agg("p50", "value_numeric", "")})},
		{"p90 value_numeric", "scores", aggDoc("scores", nil, []any{agg("p90", "value_numeric", "")})},
		{"p95 map input", "spans", aggDoc("spans", nil, []any{agg("p95", "usage_details", "input")})},
		// count/count_distinct over a partially-unset string dim (session_id set only
		// on s1) — CH must exclude the '' sentinel to match PG's NULL-skipping COUNT.
		{"count unset string", "spans", aggDoc("spans", nil, []any{agg("count", "session_id", "")})},
		{"count_distinct unset string", "spans", aggDoc("spans", nil, []any{agg("count_distinct", "session_id", "")})},
		{"count group by unset string", "spans", aggDoc("spans", []any{"model"}, []any{agg("count", "user_id", "")})},
		// traces boolean group-by — CH computes UInt8; must serialize as true/false.
		{"group by is_open", "traces", aggDoc("traces", []any{"is_open"}, []any{agg("count", "", "")})},
		{"group by incomplete_trace", "traces", aggDoc("traces", []any{"incomplete_trace"}, []any{agg("count", "", "")})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pg := runAgg(t, h.pg.QueryAggregation, tc.target, tc.doc, query.PostgresDialect)
			ch := runAgg(t, h.ch.QueryAggregation, tc.target, tc.doc, query.ClickHouseDialect)
			assertSameGroups(t, pg, ch)
		})
	}
}

// TestCrossAdapterKeyset proves keyset pagination is identical across engines: a
// page-by-page walk (limit 2 + cursor) reconstructs the exact same ordered
// sequence as one large query, on BOTH adapters, with identical page boundaries.
func TestCrossAdapterKeyset(t *testing.T) {
	h := setup(t)
	full := runPG(t, h, "spans", qdoc("spans", nil, nil)) // proven == CH full elsewhere
	pgPaged := walkPages(t, func(doc map[string]any) []json.RawMessage { return runPG(t, h, "spans", doc) })
	chPaged := walkPages(t, func(doc map[string]any) []json.RawMessage { return runCH(t, h, "spans", doc) })
	assertSameDocs(t, full, pgPaged)
	assertSameDocs(t, full, chPaged)
	assertSameDocs(t, pgPaged, chPaged)
}

// walkPages pages through spans two at a time using the compiler's cursor, the
// way the query server does (derive the next cursor from the last row's anchor+id).
func walkPages(t *testing.T, run func(map[string]any) []json.RawMessage) []json.RawMessage {
	t.Helper()
	var all []json.RawMessage
	var cursor string
	for {
		doc := qdoc("spans", nil, nil)
		doc["limit"] = float64(2)
		if cursor != "" {
			doc["cursor"] = cursor
		}
		// Compile once (PG dialect) to get the fingerprint the cursor binds to; the
		// row values themselves are dialect-neutral.
		c, err := query.CompileSpansForDialect(doc, projectID, maxWindow, query.PostgresDialect)
		if err != nil {
			t.Fatalf("compile page: %v", err)
		}
		page := run(doc)
		all = append(all, page...)
		if len(page) < 2 {
			break
		}
		last := page[len(page)-1]
		st, id := anchorOf(t, last)
		cursor = query.EncodeCursor(c.Fingerprint, st, id)
	}
	return all
}

func anchorOf(t *testing.T, raw json.RawMessage) (time.Time, string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	st, err := time.Parse(time.RFC3339Nano, m["start_time"].(string))
	if err != nil {
		t.Fatalf("parse start_time: %v", err)
	}
	return st, m["id"].(string)
}

type aggFn func(ctx context.Context, target, sel, where, groupBy string, args []any) ([]map[string]any, error)

func runAgg(t *testing.T, fn aggFn, target string, doc map[string]any, d query.Dialect) []map[string]any {
	t.Helper()
	ca, err := query.CompileAggregationForDialect(doc, projectID, maxWindow, target, d)
	if err != nil {
		t.Fatalf("compile agg: %v", err)
	}
	rows, err := fn(context.Background(), target, ca.Select, ca.Where, ca.GroupBy, ca.Args)
	if err != nil {
		t.Fatalf("agg query: %v", err)
	}
	return rows
}

// assertSameGroups compares aggregation results as SETS (aggregations have no
// orderBy, so group-row order is engine-defined). Each row is canonicalized to a
// JSON string; the multisets must match.
func assertSameGroups(t *testing.T, pg, ch []map[string]any) {
	t.Helper()
	pgSet := canonGroups(pg)
	chSet := canonGroups(ch)
	if len(pgSet) != len(chSet) {
		t.Fatalf("group count differs: pg=%d ch=%d\npg=%v\nch=%v", len(pgSet), len(chSet), pgSet, chSet)
	}
	seen := map[string]int{}
	for _, s := range pgSet {
		seen[s]++
	}
	for _, s := range chSet {
		seen[s]--
	}
	for s, n := range seen {
		if n != 0 {
			t.Errorf("group multiset differs at %q (pg-ch=%d)\npg=%v\nch=%v", s, n, pgSet, chSet)
		}
	}
}

// canonGroups renders each group row to a signature, rounding float values to 10
// significant figures. Counts/min/max/percentile/group-keys are byte-identical
// across engines; sum/avg are the one principled exception — Postgres accumulates
// in arbitrary-precision numeric, ClickHouse in float64, so the last ULP can
// differ on adversarial decimals. Rounding masks that float-associativity bound
// while still catching any real divergence.
func canonGroups(rows []map[string]any) []string {
	out := make([]string, len(rows))
	for i, m := range rows {
		rounded := make(map[string]any, len(m))
		for k, v := range m {
			if f, ok := v.(float64); ok {
				rounded[k] = strconv.FormatFloat(f, 'g', 10, 64)
			} else {
				rounded[k] = v
			}
		}
		b, _ := json.Marshal(rounded)
		out[i] = string(b)
	}
	return out
}

func runPG(t *testing.T, h *harness, target string, doc map[string]any) []json.RawMessage {
	t.Helper()
	ctx := context.Background()
	c := compile(t, target, doc, query.PostgresDialect)
	var rows []json.RawMessage
	var err error
	switch target {
	case "spans":
		rows, err = h.pg.QuerySpans(ctx, c.Where, c.Args, c.Order, c.Limit)
	case "scores":
		rows, err = h.pg.QueryScores(ctx, c.Where, c.Args, c.Order, c.Limit)
	case "traces":
		rows, err = h.pg.QueryTraces(ctx, c.Where, c.Args, c.Order, c.Limit)
	}
	if err != nil {
		t.Fatalf("pg query: %v", err)
	}
	return rows
}

func runCH(t *testing.T, h *harness, target string, doc map[string]any) []json.RawMessage {
	t.Helper()
	ctx := context.Background()
	c := compile(t, target, doc, query.ClickHouseDialect)
	var rows []json.RawMessage
	var err error
	switch target {
	case "spans":
		rows, err = h.ch.QuerySpans(ctx, c.Where, c.Args, c.Order, c.Limit)
	case "scores":
		rows, err = h.ch.QueryScores(ctx, c.Where, c.Args, c.Order, c.Limit)
	case "traces":
		rows, err = h.ch.QueryTraces(ctx, c.Where, c.Args, c.Order, c.Limit)
	}
	if err != nil {
		t.Fatalf("ch query: %v", err)
	}
	return rows
}

func compile(t *testing.T, target string, doc map[string]any, d query.Dialect) *query.Compiled {
	t.Helper()
	var c *query.Compiled
	var err error
	switch target {
	case "spans":
		c, err = query.CompileSpansForDialect(doc, projectID, maxWindow, d)
	case "scores":
		c, err = query.CompileScoresForDialect(doc, projectID, maxWindow, d)
	case "traces":
		c, err = query.CompileTracesForDialect(doc, projectID, maxWindow, d)
	}
	if err != nil {
		t.Fatalf("compile %s: %v", target, err)
	}
	return c
}

// assertSameDocs compares two result sets as canonicalized JSON, order-sensitive
// (the compiler appends a total `id` tiebreaker, so order is deterministic).
func assertSameDocs(t *testing.T, pg, ch []json.RawMessage) {
	t.Helper()
	if len(pg) != len(ch) {
		t.Fatalf("row count differs: pg=%d ch=%d\npg=%s\nch=%s", len(pg), len(ch), dump(pg), dump(ch))
	}
	for i := range pg {
		p := canon(t, pg[i])
		c := canon(t, ch[i])
		if p != c {
			t.Errorf("row %d differs:\n pg: %s\n ch: %s", i, p, c)
		}
	}
}

func canon(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("bad json %s: %v", raw, err)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func dump(rows []json.RawMessage) string {
	b, _ := json.Marshal(rows)
	return string(b)
}

// ---- DSL doc builders ----

func qdoc(target string, filters, _ []any) map[string]any {
	d := map[string]any{
		"target":    target,
		"timeRange": map[string]any{"from": ts(0).Format(time.RFC3339), "to": ts(60).Format(time.RFC3339)},
	}
	if filters != nil {
		d["filters"] = filters
	}
	return d
}

func cond(field, op string, value any) map[string]any {
	return map[string]any{"field": field, "op": op, "value": value}
}
func condOp(field, op string) map[string]any { return map[string]any{"field": field, "op": op} }
func condBool(field string, v bool) map[string]any {
	return map[string]any{"field": field, "op": "eq", "value": v}
}
func mapCond(field, key, op string, value any) map[string]any {
	return map[string]any{"field": field, "key": key, "op": op, "value": value}
}
func mapCondOp(field, key, op string) map[string]any {
	return map[string]any{"field": field, "key": key, "op": op}
}
func mapCondNum(field, key, op string, value float64) map[string]any {
	return map[string]any{"field": field, "key": key, "op": op, "value": value}
}

func aggDoc(target string, groupBy, aggs []any) map[string]any {
	d := map[string]any{
		"target":       target,
		"timeRange":    map[string]any{"from": ts(0).Format(time.RFC3339), "to": ts(60).Format(time.RFC3339)},
		"aggregations": aggs,
	}
	if groupBy != nil {
		d["groupBy"] = groupBy
	}
	return d
}

func agg(op, field, key string) map[string]any {
	a := map[string]any{"op": op}
	if field != "" {
		a["field"] = field
	}
	if key != "" {
		a["key"] = key
	}
	return a
}
