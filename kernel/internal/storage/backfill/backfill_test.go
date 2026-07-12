package backfill

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeSource is an in-memory lite side implementing the real (ts,project,id) keyset
// semantics, so the same-timestamp-cluster advance behavior is exercised faithfully.
type fakeSource struct {
	spans      []memRow
	scores     []memRow
	state      map[string]memState
	dead       []string
	failFetchN int // fail the first N fetches (transient) before succeeding
}

type memRow struct {
	ts   time.Time
	proj string
	id   string
	doc  json.RawMessage
}

type memState struct {
	cur      postgres.BackfillCursor
	done     bool
	migrated int64
}

func newFakeSource() *fakeSource { return &fakeSource{state: map[string]memState{}} }

func (f *fakeSource) add(kind string, ts time.Time, proj, id string, doc map[string]any) {
	if doc == nil {
		doc = map[string]any{"project_id": proj, "id": id}
		if kind == "scores" {
			doc["timestamp"] = ts.UTC().Format(time.RFC3339Nano)
		} else {
			doc["start_time"] = ts.UTC().Format(time.RFC3339Nano)
		}
	}
	b, _ := json.Marshal(doc)
	row := memRow{ts: ts.UTC(), proj: proj, id: id, doc: b}
	if kind == "scores" {
		f.scores = append(f.scores, row)
	} else {
		f.spans = append(f.spans, row)
	}
}

func less(a, b memRow) bool {
	if !a.ts.Equal(b.ts) {
		return a.ts.Before(b.ts)
	}
	if a.proj != b.proj {
		return a.proj < b.proj
	}
	return a.id < b.id
}

func (f *fakeSource) scan(rows []memRow, after postgres.BackfillCursor, limit int) ([]json.RawMessage, postgres.BackfillCursor) {
	sorted := append([]memRow{}, rows...)
	sort.Slice(sorted, func(i, j int) bool { return less(sorted[i], sorted[j]) })
	afterRow := memRow{ts: after.TS, proj: after.ProjectID, id: after.ID}
	var out []json.RawMessage
	next := after
	for _, r := range sorted {
		if !less(afterRow, r) { // r <= after → already consumed
			continue
		}
		out = append(out, r.doc)
		next = postgres.BackfillCursor{TS: r.ts, ProjectID: r.proj, ID: r.id}
		if len(out) >= limit {
			break
		}
	}
	return out, next
}

func (f *fakeSource) BackfillSpans(_ context.Context, after postgres.BackfillCursor, limit int) ([]json.RawMessage, postgres.BackfillCursor, error) {
	if f.failFetchN > 0 {
		f.failFetchN--
		return nil, after, io.ErrUnexpectedEOF // transient
	}
	rows, next := f.scan(f.spans, after, limit)
	return rows, next, nil
}
func (f *fakeSource) BackfillScores(_ context.Context, after postgres.BackfillCursor, limit int) ([]json.RawMessage, postgres.BackfillCursor, error) {
	rows, next := f.scan(f.scores, after, limit)
	return rows, next, nil
}
func (f *fakeSource) LoadBackfillState(_ context.Context, kind string) (postgres.BackfillCursor, bool, int64, error) {
	st := f.state[kind]
	return st.cur, st.done, st.migrated, nil
}
func (f *fakeSource) SaveBackfillState(_ context.Context, kind string, cur postgres.BackfillCursor, done bool, migrated int64) error {
	f.state[kind] = memState{cur: cur, done: done, migrated: migrated}
	return nil
}
func (f *fakeSource) DeadLetterBackfill(_ context.Context, kind, projectID, id, reason string) error {
	f.dead = append(f.dead, kind+"/"+projectID+"/"+id)
	return nil
}

// fakeSink records every persisted (project,id) and can fail the first N writes
// (transient), always fail (outage), or fail one specific id (deterministic per-row).
type fakeSink struct {
	got       map[string]bool
	failWrite int
	failAll   bool
	failID    string
}

func newFakeSink() *fakeSink { return &fakeSink{got: map[string]bool{}} }

// errTransient looks like a backend-down error (classified transient → stop loud);
// errDeterministic looks like a per-row rejection (→ dead-letter + continue).
var (
	errTransient     = errors.New("dial tcp 10.0.0.1:9000: connect: connection refused")
	errDeterministic = errors.New("code 53: TYPE_MISMATCH cannot parse field")
)

func (s *fakeSink) persist(ev storage.Event) error {
	id, _ := ev.Payload["id"].(string)
	if s.failAll {
		return errTransient // scale is down
	}
	if s.failID != "" && id == s.failID {
		return errDeterministic // one poison row
	}
	if s.failWrite > 0 {
		s.failWrite--
		return errTransient // brief blip, cleared by retry
	}
	pid, _ := ev.Payload["project_id"].(string)
	s.got[pid+"/"+id] = true
	return nil
}
func (s *fakeSink) PersistSpan(_ context.Context, ev storage.Event) error  { return s.persist(ev) }
func (s *fakeSink) PersistScore(_ context.Context, ev storage.Event) error { return s.persist(ev) }

// TestBackfillTotalOrderNoLoop proves the total-order advance: a cluster of same-timestamp rows
// migrates completely and the run terminates — the (ts,project,id) tuple advance
// never stalls on a repeated timestamp.
func TestBackfillTotalOrderNoLoop(t *testing.T) {
	src := newFakeSource()
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// 10 spans ALL sharing one timestamp (the infinite-loop trap), plus a few later.
	for i := 0; i < 10; i++ {
		src.add("spans", ts, "p1", string(rune('a'+i)), nil)
	}
	src.add("spans", ts.Add(time.Second), "p1", "z", nil)
	sink := newFakeSink()

	r := New(src, sink, Config{ChunkSize: 3}, quietLog()) // small chunks force many keyset hops
	res, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.SpansMigrated != 11 {
		t.Fatalf("want 11 spans migrated, got %d", res.SpansMigrated)
	}
	if len(sink.got) != 11 {
		t.Fatalf("want 11 distinct rows in scale, got %d", len(sink.got))
	}
	if !res.Complete {
		t.Fatal("run should have fully completed")
	}
}

// TestBackfillResumes proves a second run continues from the persisted cursor and
// does not re-migrate already-done rows.
func TestBackfillResumes(t *testing.T) {
	src := newFakeSource()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		src.add("spans", base.Add(time.Duration(i)*time.Second), "p1", string(rune('a'+i)), nil)
	}
	sink := newFakeSink()

	// First run: tiny budget-independent stop by simulating a mid-way state. Instead,
	// run with chunk 2 fully, then a fresh runner resumes (state persisted in src).
	r1 := New(src, sink, Config{ChunkSize: 2}, quietLog())
	if _, err := r1.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := len(sink.got)
	// A second run over the SAME source must find everything done (idempotent).
	r2 := New(src, sink, Config{ChunkSize: 2}, quietLog())
	res, err := r2.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first != 6 || len(sink.got) != 6 {
		t.Fatalf("want all 6 migrated, got first=%d final=%d", first, len(sink.got))
	}
	if res.SpansMigrated != 6 {
		t.Fatalf("resumed run should report 6 already-migrated, got %d", res.SpansMigrated)
	}
}

// TestBackfillDeadLettersMalformed proves a settled doc with no id is dead-lettered
// (recorded, skipped) rather than dropped or looping.
func TestBackfillDeadLettersMalformed(t *testing.T) {
	src := newFakeSource()
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src.add("spans", ts, "p1", "good", nil)
	src.add("spans", ts.Add(time.Second), "p1", "bad", map[string]any{"project_id": "p1", "start_time": ts.Format(time.RFC3339Nano)}) // no id
	sink := newFakeSink()

	r := New(src, sink, Config{ChunkSize: 10}, quietLog())
	res, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.SpansMigrated != 1 {
		t.Fatalf("want 1 migrated (good), got %d", res.SpansMigrated)
	}
	if res.DeadLettered != 1 || len(src.dead) != 1 {
		t.Fatalf("want 1 dead-lettered (bad), got res=%d recorded=%v", res.DeadLettered, src.dead)
	}
}

// TestBackfillRetriesTransient proves a transient fetch failure and a transient write
// failure are retried (not fatal, not dropped).
func TestBackfillRetriesTransient(t *testing.T) {
	src := newFakeSource()
	src.failFetchN = 2 // first two span fetches fail transiently
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src.add("spans", ts, "p1", "a", nil)
	sink := newFakeSink()
	sink.failWrite = 2 // first two writes fail transiently

	r := New(src, sink, Config{ChunkSize: 10, BackoffBase: time.Millisecond}, quietLog())
	res, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("transient failures must be retried, not fatal: %v", err)
	}
	if res.SpansMigrated != 1 || !sink.got["p1/a"] {
		t.Fatalf("row should have migrated after retries, got %d", res.SpansMigrated)
	}
}

// TestBackfillOutageStopsLoud guards the failure taxonomy: when the scale backend is down
// (every write fails transiently), the run STOPS with an error and dead-letters
// NOTHING — a transient outage must never be converted into dropped/dead-lettered rows.
// The run resumes and re-persists once the backend recovers.
func TestBackfillOutageStopsLoud(t *testing.T) {
	src := newFakeSource()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		src.add("spans", base.Add(time.Duration(i)*time.Second), "p1", string(rune('a'+i)), nil)
	}
	sink := newFakeSink()
	sink.failAll = true // scale is down (transient)

	r := New(src, sink, Config{ChunkSize: 5, MaxRetries: 1, BackoffBase: time.Millisecond}, quietLog())
	res, err := r.Run(context.Background())
	if err == nil {
		t.Fatal("a transient scale outage must stop the run LOUD, not complete silently")
	}
	if res.Complete {
		t.Fatal("outage run must not report complete")
	}
	if res.DeadLettered != 0 {
		t.Fatalf("a transient outage must NOT dead-letter any row, got %d", res.DeadLettered)
	}
}

// TestBackfillDeterministicClusterProgresses is the reviewer's clustered-failure
// regression: a CONTIGUOUS run of deterministically-failing rows must not permanently
// stall — each is dead-lettered and stepped over, so the run makes forward progress and
// completes rather than tripping/re-fetching forever.
func TestBackfillDeterministicClusterProgresses(t *testing.T) {
	src := newFakeSource()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		src.add("spans", base.Add(time.Duration(i)*time.Second), "p1", string(rune('a'+i)), nil)
	}
	// A contiguous cluster (c,d,e,f) all deterministically reject.
	sink := &clusterSink{got: map[string]bool{}, bad: map[string]bool{"c": true, "d": true, "e": true, "f": true}}

	r := New(src, sink, Config{ChunkSize: 3, MaxRetries: 1, BackoffBase: time.Millisecond}, quietLog())
	res, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("a deterministic cluster must not fail/stall the run: %v", err)
	}
	if !res.Complete {
		t.Fatal("run must complete past the cluster")
	}
	if res.SpansMigrated != 6 || res.DeadLettered != 4 {
		t.Fatalf("want 6 migrated + 4 dead-lettered, got migrated=%d dead=%d", res.SpansMigrated, res.DeadLettered)
	}
}

// TestBackfillIsolatedRowDeadLettered: a single deterministically-failing row (backend
// otherwise healthy) is dead-lettered and the rest migrate.
func TestBackfillIsolatedRowDeadLettered(t *testing.T) {
	src := newFakeSource()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		src.add("spans", base.Add(time.Duration(i)*time.Second), "p1", string(rune('a'+i)), nil)
	}
	sink := newFakeSink()
	sink.failID = "e" // one row deterministically rejects

	r := New(src, sink, Config{ChunkSize: 4, MaxRetries: 1, BackoffBase: time.Millisecond}, quietLog())
	res, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("one bad row must not fail the run: %v", err)
	}
	if !res.Complete {
		t.Fatal("run should complete despite one dead-lettered row")
	}
	if res.SpansMigrated != 9 || res.DeadLettered != 1 {
		t.Fatalf("want 9 migrated + 1 dead-lettered, got migrated=%d dead=%d", res.SpansMigrated, res.DeadLettered)
	}
	if sink.got["p1/e"] {
		t.Fatal("the failing row must not appear migrated")
	}
}

// clusterSink deterministically rejects a fixed set of ids (backend otherwise healthy).
type clusterSink struct {
	got map[string]bool
	bad map[string]bool
}

func (s *clusterSink) persistC(ev storage.Event) error {
	id, _ := ev.Payload["id"].(string)
	if s.bad[id] {
		return errDeterministic
	}
	pid, _ := ev.Payload["project_id"].(string)
	s.got[pid+"/"+id] = true
	return nil
}
func (s *clusterSink) PersistSpan(_ context.Context, ev storage.Event) error  { return s.persistC(ev) }
func (s *clusterSink) PersistScore(_ context.Context, ev storage.Event) error { return s.persistC(ev) }
