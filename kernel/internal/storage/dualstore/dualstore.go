// Package dualstore is the permanent lite↔scale dual-read layer: historical data
// stays on Postgres-lite, new data goes to ClickHouse-scale, and both are unified
// at the Query API as a designed, permanent default (not a temporary migration
// phase). It implements storage.TelemetryStore by fanning every read out to
// BOTH backends — Postgres-lite (historical) and ClickHouse-scale (new) — and
// unifying the results, so the Query API never strands a self-hoster on a
// migration cliff: a span written to scale is immediately readable, historical
// spans in lite are still readable, with NO window where either is missing.
//
// This is the ONE convergence seam every read funnels through (the kernel's
// enforce-at-the-single-seam invariant): injected once in place of the single
// adapter, it intercepts all 9 read/erase call sites by construction — including
// the implicit empty-product onboarding checks (the list + Get-404 paths). A
// forgotten existence check that still queries the old table after cutover is the
// classic dual-read data-leak trap; funneling every path through this one seam
// eliminates it by construction rather than by per-caller vigilance.
//
// Model:
//   - Writes go to SCALE (new data). If the entity already exists ONLY in lite
//     (an old entity updated after cutover), its settled doc is replayed into
//     scale as a synthetic event FIRST (seed-on-migrate), so scale holds the
//     complete entity — no field is lost by folding a new event onto an empty row.
//   - Read dedup prefers SCALE for any (project_id,id) present in both: a
//     duplicated entity was migrated, so scale's copy is the complete/newer one.
//     A distinct entity is returned from whichever store holds it.
package dualstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// Store is the dual-read decorator over two backends for the NON-compiled-SQL
// paths: writes (seed-on-migrate), point reads (Get*), the trace-span union, and
// erasure. The compiled-SQL query paths (spans/scores/traces/aggregation lists)
// are dialect-specific, so they cannot fan one compiled statement to two engines;
// those live in the query.DualRouter, which compiles per dialect and merges via the
// exported helpers in this package (mergeOrdered/SynthesizeTrace/MergeAggregation).
type Store struct {
	lite  storage.TelemetryStore // historical (read-only in steady state)
	scale storage.TelemetryStore // new data + all writes
}

// New builds the dual-read store. lite holds historical data, scale takes writes.
func New(lite, scale storage.TelemetryStore) *Store {
	return &Store{lite: lite, scale: scale}
}

// The dual store IS a TelemetryStore (so it can be the pipeline write target); its
// compiled-SQL read methods fail loud — reads route through query.DualRouter.
var _ storage.TelemetryStore = (*Store)(nil)

// ---- writes: scale, with seed-on-migrate ----

func (s *Store) PersistSpan(ctx context.Context, ev storage.Event) error {
	return s.persist(ctx, ev, s.scale.PersistSpan, s.lite.GetSpan)
}

func (s *Store) PersistScore(ctx context.Context, ev storage.Event) error {
	return s.persist(ctx, ev, s.scale.PersistScore, s.lite.GetScore)
}

// persist writes to scale. If the entity is absent from scale but present in lite,
// it first replays lite's settled doc into scale as a synthetic event so the fold
// starts from the complete historical state (seed-on-migrate) — otherwise folding
// the new event onto scale's empty row would drop every field only lite knew.
func (s *Store) persist(
	ctx context.Context,
	ev storage.Event,
	scalePersist func(context.Context, storage.Event) error,
	liteGet func(context.Context, string, string) (json.RawMessage, error),
) error {
	projectID, _ := ev.Payload["project_id"].(string)
	id, _ := ev.Payload["id"].(string)
	if projectID != "" && id != "" {
		// Only migrate if scale doesn't already have it AND lite does. GetSpan/GetScore
		// on scale is a cheap point read; the lite check only runs on a scale miss.
		if scaleDoc, _ := s.getScale(ctx, ev, id, projectID); scaleDoc == nil {
			if liteDoc, _ := liteGet(ctx, projectID, id); liteDoc != nil {
				if seed, ok := syntheticEvent(liteDoc); ok {
					if err := scalePersist(ctx, seed); err != nil {
						return err
					}
				}
			}
		}
	}
	return scalePersist(ctx, ev)
}

// getScale point-reads the entity from scale using the right accessor for its kind.
func (s *Store) getScale(ctx context.Context, ev storage.Event, id, projectID string) (json.RawMessage, error) {
	// A span event has a trace_id/kind; a score has subject_type. Cheapest: try the
	// matching Get. We infer kind from the presence of score-only fields.
	if _, isScore := ev.Payload["subject_type"]; isScore {
		return s.scale.GetScore(ctx, projectID, id)
	}
	return s.scale.GetSpan(ctx, projectID, id)
}

// syntheticEvent turns a settled doc into an upsert event whose EventTS is the
// entity's anchor time (historical, so a real post-cutover event always wins on a
// conflicting field). Re-folding a settled doc yields itself, so this faithfully
// seeds scale with lite's state.
func syntheticEvent(doc json.RawMessage) (storage.Event, bool) {
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		return storage.Event{}, false
	}
	id, _ := m["id"].(string)
	if id == "" {
		return storage.Event{}, false
	}
	return storage.Event{
		Op:      storage.OpUpsert,
		EventTS: anchorTime(m),
		EventID: id + "@seed",
		Payload: m,
	}, true
}

// ---- reads: fan out to both, unify ----

func (s *Store) GetSpan(ctx context.Context, projectID, id string) (json.RawMessage, error) {
	return s.getOne(ctx, projectID, id, s.scale.GetSpan, s.lite.GetSpan)
}

func (s *Store) GetScore(ctx context.Context, projectID, id string) (json.RawMessage, error) {
	return s.getOne(ctx, projectID, id, s.scale.GetScore, s.lite.GetScore)
}

// getOne prefers scale: a migrated/new entity lives in scale; a historical-only
// entity is served from lite.
func (s *Store) getOne(
	ctx context.Context, projectID, id string,
	scaleGet, liteGet func(context.Context, string, string) (json.RawMessage, error),
) (json.RawMessage, error) {
	doc, err := scaleGet(ctx, projectID, id)
	if err != nil {
		return nil, err
	}
	if doc != nil {
		return doc, nil
	}
	return liteGet(ctx, projectID, id)
}

// Backends exposes the two underlying stores so the query.DualRouter can compile
// per-dialect SQL and merge (the compiled-SQL query paths that can't live on this
// dialect-agnostic decorator).
func (s *Store) Backends() (lite, scale storage.TelemetryStore) { return s.lite, s.scale }

// ---- compiled-SQL query methods: NOT servable here (fail loud) ----
//
// The Store satisfies storage.TelemetryStore so it can be the pipeline's write
// target (PersistSpan seed-on-migrate). But the four compiled-SQL read methods take
// dialect-SPECIFIC SQL ($1 vs ?), so ONE compiled statement cannot fan to both
// engines — the query server routes list/aggregation reads through query.DualRouter
// (which compiles per dialect via Backends() and merges) instead. If one of these is
// ever reached, a caller bypassed the router: fail loud rather than send Postgres SQL
// to ClickHouse (the "mixed named/numeric parameters" corruption we hit in review).
var errUseDualRouter = errors.New("dualstore: compiled-SQL reads must go through the query DualRouter (per-dialect compile + merge), not the TelemetryStore interface")

func (s *Store) QuerySpans(context.Context, string, []any, string, int) ([]json.RawMessage, error) {
	return nil, errUseDualRouter
}
func (s *Store) QueryTraces(context.Context, string, []any, string, int) ([]json.RawMessage, error) {
	return nil, errUseDualRouter
}
func (s *Store) QueryScores(context.Context, string, []any, string, int) ([]json.RawMessage, error) {
	return nil, errUseDualRouter
}
func (s *Store) QueryAggregation(context.Context, string, string, string, string, []any) ([]map[string]any, error) {
	return nil, errUseDualRouter
}

func (s *Store) GetTraceSpans(ctx context.Context, projectID, traceID string) ([]json.RawMessage, error) {
	scaleSpans, err := s.scale.GetTraceSpans(ctx, projectID, traceID)
	if err != nil {
		return nil, err
	}
	liteSpans, err := s.lite.GetTraceSpans(ctx, projectID, traceID)
	if err != nil {
		return nil, err
	}
	// Union the trace's spans across both stores, dedup by id (prefer scale), and
	// re-sort by (start_time, id) so the tree assembler still sees preorder.
	union := dedupByID(append(append([]json.RawMessage{}, scaleSpans...), liteSpans...))
	sort.SliceStable(union, func(i, j int) bool {
		ai, aj := docAnchorID(union[i]), docAnchorID(union[j])
		if ai.ts.Equal(aj.ts) {
			return ai.id < aj.id
		}
		return ai.ts.Before(aj.ts)
	})
	return union, nil
}

// EraseIDResolver (the lite side) reports the span ids an erase filter WOULD remove
// without deleting, so the dual store can pre-suppress lite-only ids in scale before
// any physical delete.
type EraseIDResolver interface {
	SpanIDsForErase(ctx context.Context, projectID, userID string, from, to time.Time) ([]string, error)
}

// EraseSuppressor (the scale side) records suppression tombstones for explicit ids
// without deleting.
type EraseSuppressor interface {
	SuppressSpans(ctx context.Context, projectID string, ids []string, auditID string) error
}

// EraseSpans erases in BOTH stores (an erased user's spans may be split across the
// boundary) and returns the combined count + the scale audit id.
//
// RESURRECTION GUARD (security). A span present ONLY in lite is not resolved by
// scale's own erase, so scale would keep NO suppression tombstone for it — and the
// backfill/seed could later replay that lite doc (possibly already read into an
// in-flight batch) into scale, resurrecting erased data. To close this at the seam
// scale.PersistSpan already guards (its suppression check), we make scale's
// suppression set complete: resolve lite's matching ids and suppress them in scale
// BEFORE any delete. Ordered suppress→scale-erase→lite-erase so that a concurrent
// replay is either blocked by the tombstone (write after suppress) or physically
// removed by scale's erase (write before suppress) — never left resurrected.
func (s *Store) EraseSpans(ctx context.Context, projectID, userID, actor string, from, to time.Time) (int, string, error) {
	// FAIL CLOSED: erasure MUST NOT proceed without the cross-boundary guard, or a
	// lite-only span could be erased with no scale tombstone and later resurrected. If
	// a future decorator wraps either store and drops the capability, refuse the erase
	// loudly rather than silently degrade the GDPR guarantee.
	resolver, ok := s.lite.(EraseIDResolver)
	if !ok {
		return 0, "", errors.New("dualstore: lite store lacks EraseIDResolver; refusing to erase without the cross-boundary resurrection guard")
	}
	suppressor, ok := s.scale.(EraseSuppressor)
	if !ok {
		return 0, "", errors.New("dualstore: scale store lacks EraseSuppressor; refusing to erase without the cross-boundary resurrection guard")
	}
	liteIDs, err := resolver.SpanIDsForErase(ctx, projectID, userID, from, to)
	if err != nil {
		return 0, "", err
	}
	if len(liteIDs) > 0 {
		guardID, err := suppressAuditID()
		if err != nil {
			return 0, "", err
		}
		if err := suppressor.SuppressSpans(ctx, projectID, liteIDs, guardID); err != nil {
			return 0, "", err
		}
	}
	scaleN, auditID, err := s.scale.EraseSpans(ctx, projectID, userID, actor, from, to)
	if err != nil {
		return 0, "", err
	}
	liteN, _, err := s.lite.EraseSpans(ctx, projectID, userID, actor, from, to)
	if err != nil {
		return 0, "", err
	}
	return scaleN + liteN, auditID, nil
}

// suppressAuditID mints the audit id stamped on the cross-boundary resurrection-guard
// suppression tombstones (distinct from the scale erase's own audit id, which is
// returned to the caller).
func suppressAuditID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "era_dualguard_" + hex.EncodeToString(buf), nil
}
