// Package storage defines the telemetry storage boundary the kernel dataplane
// depends on (D7, 99-adapter-guidance.md). The lite adapter
// (internal/storage/postgres) and any future adapter (ClickHouse, Timescale)
// implement these interfaces; the dataplane (query, pipeline) depends only on
// them, never on a concrete adapter. This is the seam the flexibility audit found
// documented but absent in code.
//
// It is internal/ (not pkg/) by construction: adapters are a kernel-internal
// concern — plugins never touch storage (invariant 3, D3). The neutral ingest
// Event type lives here too, since it is a canonical-model concept, not a
// Postgres one.
//
// Control-plane storage (users, api keys, projects, sessions) is deliberately
// NOT part of this interface — it is a separate concern and is accessed directly
// via the pool. Do not fuse the two.
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

// CostRoundDecimals is the fixed decimal scale the DERIVED trace-level total_cost is
// rounded to in EVERY adapter (Postgres, ClickHouse) and the dual-read re-synthesis, so
// the roll-up is BYTE-IDENTICAL cross-adapter (the strict row-projection contract) rather
// than diverging in float low bits: Postgres sums exact NUMERIC while ClickHouse/Go
// accumulate float64 (0.04+0.05 → 0.09 vs 0.09000000000000001). 10 decimals is far below
// any billing granularity (0.1 nano-USD) and, for realistic trace costs, within float64's
// exact-integer range after scaling, so the rounding is deterministic and lossless in
// practice — and identical rounding of both engines' sums collapses the divergence to zero.
const CostRoundDecimals = 10

// CostRoundSQL is the scale literal for the adapters' round(...) calls.
func CostRoundSQL() string { return strconv.Itoa(CostRoundDecimals) }

// RoundCost rounds a trace-level cost to CostRoundDecimals (the dual-read re-synth uses
// this so a split trace's total_cost matches a single-store one bit-for-bit).
func RoundCost(c float64) float64 {
	p := math.Pow(10, CostRoundDecimals)
	return math.Round(c*p) / p
}

// AggregateKinds are the span kinds whose total_cost is EXCLUDED from trace-level cost
// roll-ups (06-usage-cost.md §7.1). An agent_step / tool_call span frequently carries
// usage — and thus cost — that duplicates its child model-call spans; summing it into
// the trace total would double-count. This is the query-time counterpart to the ingest
// gate (normalize.isAggregateUsageSpan / #81, which strips such spans' usage): trace
// cost accrues only on the non-aggregate (leaf model-call) spans. Defined ONCE so every
// adapter's trace projection (Postgres, ClickHouse) AND the dual-read re-synthesis
// exclude the SAME set — a divergence would fork trace cost per engine (the
// classify-by-metadata-consistently lesson). Keep in sync with the kinds
// normalize.mapKind emits for aggregate ops.
var AggregateKinds = []string{"agent_step", "tool_call"}

// AggregateKindsSQL renders AggregateKinds as a SQL IN-list literal
// ("'agent_step', 'tool_call'") so both adapters build the same predicate from one
// source. The values are fixed internal constants (never user data), safe to inline.
func AggregateKindsSQL() string {
	q := make([]string, len(AggregateKinds))
	for i, k := range AggregateKinds {
		q[i] = "'" + k + "'"
	}
	return strings.Join(q, ", ")
}

// IsAggregateKind reports whether a span kind is excluded from trace cost roll-ups.
func IsAggregateKind(kind string) bool {
	for _, k := range AggregateKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// ErrSuppressedByErasure is returned by PersistSpan when an incoming span matches
// an unexpired erasure-suppression tombstone (G3): the span was GDPR-erased and a
// re-delivery must NOT resurrect it. It is an expected outcome, not a storage
// failure — the persist stage counts it and moves on, and it MUST NOT be folded
// into persist-health (it is not a sign the adapter is unwell).
var ErrSuppressedByErasure = errors.New("span suppressed by erasure tombstone")

// Op is an ingested event operation.
type Op string

const (
	OpUpsert Op = "upsert"
	OpDelete Op = "delete"
)

// Event is one ingested event targeting a single entity (05-update-semantics.md
// §1). The dataplane builds these in the normalize stage; the adapter folds them.
type Event struct {
	Op      Op
	EventTS time.Time
	EventID string
	Payload map[string]any
}

// TelemetryStore is the persistence + read surface the dataplane requires,
// derived from the actual call sites in query/pipeline (interface-at-consumer,
// kept in one named contract so an adapter author has a single thing to
// implement). Query methods take a compiled predicate + args: the lite adapter's
// DSL→SQL compiler emits Postgres SQL, which a Postgres-compatible adapter
// (Timescale) reuses as-is. Relocating compilation behind an adapter-owned
// compile step (ADR-0019) is the ClickHouse-driven follow-up, not this refactor.
// Adapter read contract (K1.5): the DSL read methods (QuerySpans/QueryTraces/
// QueryScores/QueryAggregation) MUST enforce a server-side statement timeout, not
// only honor ctx cancellation — a client-side cancel stops the client, not the
// server, so a lost/late cancel would otherwise let a pathological plan run
// unbounded. The lite adapter uses `SET LOCAL statement_timeout` in a read
// transaction (auto-resets, never poisons a pooled connection); a ClickHouse
// adapter MUST set `max_execution_time` equivalently. The DSL's max-window +
// ceilings bound query *shape*; this bounds query *execution time*.
type TelemetryStore interface {
	// PersistSpan applies one span event with merge-on-write (LM-5).
	PersistSpan(ctx context.Context, ev Event) error
	// QuerySpans runs a compiled spans predicate and returns folded span docs.
	QuerySpans(ctx context.Context, where string, args []any, order string, limit int) ([]json.RawMessage, error)
	// QueryTraces runs a compiled traces predicate over the synthesized trace
	// projection and returns trace docs (DSL §4.1).
	QueryTraces(ctx context.Context, where string, args []any, order string, limit int) ([]json.RawMessage, error)
	// GetSpan returns a folded span doc by id, or nil if absent/deleted.
	GetSpan(ctx context.Context, projectID, id string) (json.RawMessage, error)
	// GetTraceSpans returns a trace's non-deleted spans in tree-buildable order.
	GetTraceSpans(ctx context.Context, projectID, traceID string) ([]json.RawMessage, error)

	// PersistScore applies one score event with merge-on-write (LM-3/LM-8).
	PersistScore(ctx context.Context, ev Event) error
	// QueryScores runs a compiled scores predicate and returns folded score docs.
	QueryScores(ctx context.Context, where string, args []any, order string, limit int) ([]json.RawMessage, error)
	// GetScore returns a folded score doc by id, or nil if absent/deleted.
	GetScore(ctx context.Context, projectID, id string) (json.RawMessage, error)

	// EraseSpans hard-deletes spans for (projectID, userID) within [from, to) and
	// records an erasure audit row; returns the count erased and the audit id.
	EraseSpans(ctx context.Context, projectID, userID, actor string, from, to time.Time) (int, string, error)

	// QueryAggregation runs a compiled aggregation (QD-4) for a target and returns
	// group rows as column-name -> value maps. sel/where/groupBy come from the
	// aggregation compiler; the adapter supplies the FROM source per target.
	QueryAggregation(ctx context.Context, target, sel, where, groupBy string, args []any) ([]map[string]any, error)
}

// RepriceCursor is the TOTAL order a re-pricing run resumes from: (ts, project_id, id).
// The tuple is fully ordering, so a keyset scan strictly advances and same-timestamp
// spans never loop (the #7117 trap). The zero cursor starts from the beginning. It is
// dialect-neutral so both adapters' SpansForReprice share one resumable contract.
type RepriceCursor struct {
	TS        time.Time
	ProjectID string
	ID        string
}

// RepriceFilter selects the derived spans a re-pricing run re-derives (06-usage-cost.md
// §5). At least one field is always set, so a run never scans the whole table:
//   - SnapshotRefID: spans priced against a specific superseded price-version id
//     ("openai/gpt-4o#1"). GLOBAL across projects — a price change applies instance-wide.
//   - ProjectID: constrain to one project — set ALONE for a discount change, or with
//     SnapshotRefID to scope a price re-price to one tenant. When set, the scan provably
//     cannot cross the project boundary (the tenant-isolation guarantee).
type RepriceFilter struct {
	SnapshotRefID string
	ProjectID     string
}

// RepriceScanner is the TELEMETRY surface the re-pricing backfill drives: a resumable
// scan of derived spans + the persist it re-emits through. BOTH adapters (Postgres,
// ClickHouse) implement it, so re-pricing runs in either profile against the store the
// spans live in. Declared at the consumer.
type RepriceScanner interface {
	SpansForReprice(ctx context.Context, runKey string, f RepriceFilter, after RepriceCursor, limit int) ([]json.RawMessage, RepriceCursor, error)
	PersistSpan(ctx context.Context, ev Event) error
}

// RepriceState is the CONTROL-PLANE surface for a run's resumable cursor + dead-letter.
// It is ALWAYS Postgres (control-plane metadata lives in Postgres in both profiles), so
// it is a separate interface from the adapter-specific RepriceScanner — a ClickHouse
// re-price uses a ClickHouse scanner but this Postgres-backed state.
type RepriceState interface {
	LoadRepriceState(ctx context.Context, runKey string) (cur RepriceCursor, repriced, scanned int64, err error)
	SaveRepriceState(ctx context.Context, runKey string, cur RepriceCursor, repriced, scanned int64) error
	// ClearRepriceState deletes a run's cursor on completion, so the state row exists ONLY
	// while a run is in-flight (for crash-resumption). A later re-trigger of the same run
	// key therefore starts fresh and re-scans — catching a SUBSEQUENT change (a second
	// discount edit reuses the run key) instead of being short-circuited as "already done".
	ClearRepriceState(ctx context.Context, runKey string) error
	DeadLetterReprice(ctx context.Context, runKey, projectID, id, reason string) error
}

// MergeConformer is the normative-merge surface an adapter exposes to the
// conformance harness (tools/conformance). Every adapter MUST reproduce the fold
// in 05-update-semantics.md; the harness asserts that with the spec's V-vectors
// and the order-independence property. Fold is the pure ordered fold;
// MergeIncremental applies events one-by-one (the read-modify-write path) and
// MUST equal Fold over the same set regardless of arrival order.
type MergeConformer interface {
	Name() string
	Fold(entityType string, events []Event) map[string]any
	MergeIncremental(entityType string, events []Event) map[string]any
}
