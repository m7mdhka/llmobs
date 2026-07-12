package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/merge"
)

// Store is the lite-profile storage adapter.
type Store struct {
	pool           *pgxpool.Pool
	suppressionTTL time.Duration // erasure-tombstone retention window
	queryTimeout   time.Duration // server-side statement_timeout for DSL reads (0 = off)
}

// defaultSuppressionTTL retains erasure tombstones long enough to outlast
// plausible redelivery (Collector/Kafka replay), then they may be reaped.
const defaultSuppressionTTL = 720 * time.Hour // 30 days

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, suppressionTTL: defaultSuppressionTTL}
}

// SetErasureSuppressionTTL overrides how long erasure tombstones block
// re-delivery of an erased span. Non-positive values are ignored.
func (s *Store) SetErasureSuppressionTTL(d time.Duration) {
	if d > 0 {
		s.suppressionTTL = d
	}
}

// SetQueryTimeout sets a server-side statement_timeout applied to DSL read queries
// (a backstop so a client cancel or a pathological plan cannot run unbounded — a
// client-side timeout only stops the *client*, not the server). Non-positive
// disables it (queries run directly on the pool). This is a REQUIRED behavior of any
// storage adapter's read path (the ClickHouse adapter must set `max_execution_time`).
func (s *Store) SetQueryTimeout(d time.Duration) {
	if d > 0 {
		s.queryTimeout = d
	}
}

// queryRead runs a read query under the configured server-side statement_timeout and
// returns the rows plus a done() the caller MUST defer (it closes the rows and ends
// the bounding transaction). When no timeout is configured it runs directly on the
// pool. SET LOCAL scopes the timeout to this transaction and auto-resets at its end,
// so a pooled connection is never left with a lingering timeout.
func (s *Store) queryRead(ctx context.Context, sql string, args ...any) (pgx.Rows, func(), error) {
	if s.queryTimeout <= 0 {
		rows, err := s.pool.Query(ctx, sql, args...)
		if err != nil {
			return nil, nil, err
		}
		return rows, func() { rows.Close() }, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	// The value is a kernel-controlled integer (milliseconds), never user input.
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = "+itoa(int(s.queryTimeout.Milliseconds()))); err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, err
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, err
	}
	// Read-only: rollback (not commit) releases the connection with no side effects.
	return rows, func() { rows.Close(); _ = tx.Rollback(ctx) }, nil
}

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// PersistSpan applies one span event with **merge-on-write under the row lock**:
// read the current row FOR UPDATE, fold the current
// state together with the incoming event, and write the result. On first sight of
// an id, the folded single-event state is inserted.
func (s *Store) PersistSpan(ctx context.Context, ev storage.Event) error {
	projectID, _ := ev.Payload["project_id"].(string)
	id, _ := ev.Payload["id"].(string)
	if projectID == "" || id == "" {
		return errMissingIdentity
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var existingDoc, existingProv []byte
	err = tx.QueryRow(ctx,
		`SELECT doc, provenance FROM spans WHERE project_id=$1 AND id=$2 FOR UPDATE`,
		projectID, id).Scan(&existingDoc, &existingProv)

	state := map[string]any{}
	prov := merge.Provenance{}
	switch err {
	case nil:
		if uerr := json.Unmarshal(existingDoc, &state); uerr != nil {
			return uerr
		}
		if len(existingProv) > 0 {
			if uerr := json.Unmarshal(existingProv, &prov); uerr != nil {
				return uerr
			}
		}
	case pgx.ErrNoRows:
		// first sight of this id: fold against empty state+provenance
	default:
		return err
	}

	// Per-field provenance fold under the row lock: incoming event
	// folds against per-group stamps, so out-of-order updates converge to the
	// same state as the ordered Fold.
	merged, newProv := merge.MergeEvent("span", state, prov, ev)

	doc, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	provJSON, err := json.Marshal(newProv)
	if err != nil {
		return err
	}
	c := extractSpanColumns(merged, ev.EventTS)
	// Erasure suppression: the write is guarded by NOT EXISTS against an
	// unexpired tombstone, so a re-delivery of a GDPR-erased span inserts zero rows
	// (detected below) instead of resurrecting it. The guard is atomic with the
	// upsert, and the spans row lock (held from the SELECT ... FOR UPDATE above)
	// serializes against a concurrent erasure's DELETE — either the span is deleted
	// after this write, or this write sees the committed tombstone and no-ops.
	tag, err := tx.Exec(ctx, `
		INSERT INTO spans (project_id, id, trace_id, parent_span_id, kind, raw_kind, name,
			start_time, end_time, completion_start_time, status_code, environment, release, version, session_id, user_id,
			model, provider, total_cost, attributes, usage_details, cost_details,
			provided_usage_details, provided_cost_details, prompt_ref, pricing_snapshot_ref,
			is_deleted, event_ts, doc, provenance, updated_at)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,
			$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30, now()
		WHERE NOT EXISTS (
			SELECT 1 FROM erasure_suppression es
			WHERE es.project_id=$1 AND es.id=$2 AND es.expires_at > now())
		ON CONFLICT (project_id, id) DO UPDATE SET
			trace_id=EXCLUDED.trace_id, parent_span_id=EXCLUDED.parent_span_id, kind=EXCLUDED.kind,
			raw_kind=EXCLUDED.raw_kind, name=EXCLUDED.name, start_time=EXCLUDED.start_time,
			end_time=EXCLUDED.end_time, completion_start_time=EXCLUDED.completion_start_time,
			status_code=EXCLUDED.status_code, environment=EXCLUDED.environment,
			release=EXCLUDED.release, version=EXCLUDED.version, session_id=EXCLUDED.session_id,
			user_id=EXCLUDED.user_id, model=EXCLUDED.model, provider=EXCLUDED.provider,
			total_cost=EXCLUDED.total_cost, attributes=EXCLUDED.attributes, usage_details=EXCLUDED.usage_details,
			cost_details=EXCLUDED.cost_details, provided_usage_details=EXCLUDED.provided_usage_details,
			provided_cost_details=EXCLUDED.provided_cost_details, prompt_ref=EXCLUDED.prompt_ref,
			pricing_snapshot_ref=EXCLUDED.pricing_snapshot_ref, is_deleted=EXCLUDED.is_deleted,
			event_ts=EXCLUDED.event_ts, doc=EXCLUDED.doc, provenance=EXCLUDED.provenance, updated_at=now()`,
		projectID, id, c.traceID, c.parentSpanID, c.kind, c.rawKind, c.name,
		c.startTime, c.endTime, c.completionStartTime, c.statusCode, c.environment, c.release, c.version, c.sessionID, c.userID,
		c.model, c.provider, c.totalCost, c.attributes, c.usageDetails, c.costDetails,
		c.providedUsageDetails, c.providedCostDetails, c.promptRef, c.pricingSnapshotRef,
		c.isDeleted, ev.EventTS.UTC(), doc, provJSON)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// The NOT EXISTS guard fired: an unexpired erasure tombstone suppresses this
		// key. Report it as the expected sentinel (not a storage failure) so the
		// caller drops the span and does not mark persistence unhealthy.
		return storage.ErrSuppressedByErasure
	}
	return tx.Commit(ctx)
}

// (merge-on-write folds via MergeEvent above; see merge.go for the fold.)

// spansSuppressionExclusion excludes any span whose (project_id, id) carries an
// UNEXPIRED erasure-suppression tombstone — the read-side half of the erasure
// guarantee, symmetric with the ClickHouse adapter. The write path guards inserts with a
// NOT EXISTS against this table, but that guard races a tombstone committed after the
// insert's snapshot (a concurrent erase during a backfill/re-ingest): the span lands with
// is_deleted=false and would otherwise be readable — a resurrected erased span. Excluding
// suppressed ids at read time closes it for every interleaving, bounded to the tombstone
// TTL by now() so a legitimately re-created id reads again after the retention window.
// Correlated NOT EXISTS: no new bind, and it appends after `is_deleted=false` wherever
// the FROM is the bare `spans` table.
//
// EVERY spans-read site MUST carry this (query shapes differ, so it is placed per-site,
// not at one seam — a new spans read must add it by this checklist): GetSpan, QuerySpans,
// GetTraceSpans, the traceProjection span_base CTE, and QueryAggregation's spans target.
const spansSuppressionExclusion = ` AND NOT EXISTS (SELECT 1 FROM erasure_suppression es ` +
	`WHERE es.project_id = spans.project_id AND es.id = spans.id AND es.expires_at > now())`

// GetSpan returns the folded span document by id, or nil if absent/deleted.
func (s *Store) GetSpan(ctx context.Context, projectID, id string) (json.RawMessage, error) {
	var doc []byte
	err := s.pool.QueryRow(ctx,
		`SELECT doc FROM spans WHERE project_id=$1 AND id=$2 AND is_deleted=false`+spansSuppressionExclusion,
		projectID, id).Scan(&doc)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return doc, err
}

// traceProjection synthesizes one row per trace from its spans: times
// span earliest start to latest end, dimensions come from the root span (parent
// empty, tie-broken by earliest (start_time,id); else the earliest span), status is
// `error` if any span errored, and total_cost sums only NON-aggregate spans' cost
// (an agent_step/tool_call duplicates its children's usage, so summing it would
// double-count). Built once from storage.AggregateKinds so the excluded-kinds set is
// shared with the ClickHouse projection and the dual-read re-synthesis. `SUM … FILTER`
// ignores NULL costs and aggregate kinds and is NULL when a trace has no leaf cost
// (the ClickHouse projection matches this NULL-when-none). This is the query-time
// materialization — traces have no write path.
var traceProjection = `
WITH span_base AS (
	SELECT * FROM spans WHERE is_deleted = false` + spansSuppressionExclusion + `
),
roots AS (
	SELECT DISTINCT ON (trace_id)
		trace_id, name, environment, release, version, session_id, user_id, status_code, attributes
	FROM span_base
	ORDER BY trace_id,
		(parent_span_id IS NOT NULL AND parent_span_id <> '') ASC,
		start_time ASC, id ASC
),
agg AS (
	SELECT sb.trace_id, sb.project_id,
		MIN(sb.start_time) AS start_time,
		MAX(sb.end_time)   AS end_time,
		bool_or(sb.status_code = 'error') AS any_error,
		bool_or(sb.end_time IS NULL) AS is_open,
		-- last_activity: the most recent known time (max end_time, else max start).
		COALESCE(MAX(sb.end_time), MAX(sb.start_time)) AS last_activity,
		-- trace-level cost (§7.1): sum non-aggregate spans' cost only; NULL if no leaf cost.
		-- COALESCE(kind,'') so a NULL kind is treated as non-aggregate (matches ClickHouse,
		-- whose kind defaults to ''); round to the shared scale so the sum is byte-identical
		-- to the ClickHouse/dual-read float64 accumulation (PG sums exact NUMERIC).
		round(SUM(sb.total_cost) FILTER (WHERE COALESCE(sb.kind,'') NOT IN (` + storage.AggregateKindsSQL() + `)), ` + storage.CostRoundSQL() + `) AS total_cost,
		-- incomplete_trace (llmobs.dq): a span references a parent absent from the
		-- trace (Collector tail-sampling dropped it). Orphan-with-parent-ref.
		bool_or(
			sb.parent_span_id IS NOT NULL AND sb.parent_span_id <> ''
			AND NOT EXISTS (
				SELECT 1 FROM span_base p
				WHERE p.project_id = sb.project_id AND p.trace_id = sb.trace_id AND p.id = sb.parent_span_id
			)
		) AS incomplete_trace,
		count(*) AS span_count
	FROM span_base sb
	GROUP BY sb.trace_id, sb.project_id
)
SELECT
	a.trace_id AS id, a.project_id, a.start_time, a.end_time, a.last_activity,
	CASE WHEN a.any_error THEN 'error' ELSE r.status_code END AS status_code,
	r.name, r.environment, r.release, r.version, r.session_id, r.user_id,
	r.attributes, a.span_count, a.is_open, a.incomplete_trace, a.total_cost
FROM agg a JOIN roots r ON r.trace_id = a.trace_id`

// QueryTraces runs a compiled traces query against the synthesized projection and
// returns the trace documents. where/order/limit come from CompileTraces and
// reference the projection's columns.
func (s *Store) QueryTraces(ctx context.Context, where string, args []any, order string, limit int) ([]json.RawMessage, error) {
	// COALESCE nullable dimension columns to '' so scanning is simple; the
	// filter/order predicate still sees the real values (it runs on trace_proj,
	// where NULLs are preserved for NULL-policy semantics).
	sql := "WITH trace_proj AS (" + traceProjection + ") SELECT " +
		"id, project_id, start_time, end_time, last_activity, " +
		"COALESCE(status_code,''), COALESCE(name,''), COALESCE(environment,''), " +
		"COALESCE(release,''), COALESCE(version,''), COALESCE(session_id,''), COALESCE(user_id,''), " +
		"attributes, span_count, is_open, incomplete_trace, total_cost " +
		"FROM trace_proj"
	if where != "" {
		sql += " WHERE " + where
	}
	if order != "" {
		sql += " ORDER BY " + order
	}
	sql += " LIMIT " + itoa(limit)
	rows, done, err := s.queryRead(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer done()
	budget := storage.NewResponseBudget(ctx)
	var out []json.RawMessage
	for rows.Next() {
		var (
			id, projectID, statusCode, name, environment, release, version, sessionID, userID string
			startTime                                                                         time.Time
			endTime, lastActivity                                                             *time.Time
			attributes                                                                        []byte
			spanCount                                                                         int64
			isOpen, incompleteTrace                                                           bool
			totalCost                                                                         *float64
		)
		if err := rows.Scan(&id, &projectID, &startTime, &endTime, &lastActivity, &statusCode, &name,
			&environment, &release, &version, &sessionID, &userID, &attributes, &spanCount, &isOpen, &incompleteTrace, &totalCost); err != nil {
			return nil, err
		}
		doc := map[string]any{
			"id":          id,
			"project_id":  projectID,
			"name":        name,
			"start_time":  startTime.UTC().Format(time.RFC3339Nano),
			"status":      map[string]any{"code": statusCode},
			"environment": environment,
			"release":     release,
			"version":     version,
			"session_id":  sessionID,
			"user_id":     userID,
			"tags":        []any{},
			"span_count":  spanCount,
			"is_open":     isOpen,
		}
		if incompleteTrace {
			doc["llmobs.dq.incomplete_trace"] = true
		}
		if lastActivity != nil {
			doc["last_activity"] = lastActivity.UTC().Format(time.RFC3339Nano)
		}
		if endTime != nil {
			doc["end_time"] = endTime.UTC().Format(time.RFC3339Nano)
		}
		if totalCost != nil {
			doc["total_cost"] = *totalCost
		}
		if len(attributes) > 0 {
			var attrs map[string]any
			if json.Unmarshal(attributes, &attrs) == nil {
				doc["attributes"] = attrs
			}
		}
		b, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		if err := budget.Add(len(b)); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetTraceSpans returns all non-deleted spans of a trace (folded docs). Ordered
// by (start_time, id) so the tree assembler produces a deterministic preorder.
func (s *Store) GetTraceSpans(ctx context.Context, projectID, traceID string) ([]json.RawMessage, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT doc FROM spans WHERE project_id=$1 AND trace_id=$2 AND is_deleted=false`+spansSuppressionExclusion+`
		 ORDER BY start_time ASC, id ASC`, projectID, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	budget := storage.NewResponseBudget(ctx)
	var out []json.RawMessage
	for rows.Next() {
		var doc []byte
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		if err := budget.Add(len(doc)); err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, rows.Err()
}

// QuerySpans runs a compiled spans query and returns the folded documents.
// where is a SQL predicate (excluding project/is_deleted, added here); args are
// its parameters starting at $1; order and limit come from the compiler.
func (s *Store) QuerySpans(ctx context.Context, where string, args []any, order string, limit int) ([]json.RawMessage, error) {
	sql := `SELECT doc FROM spans WHERE is_deleted=false` + spansSuppressionExclusion
	if where != "" {
		sql += " AND (" + where + ")"
	}
	if order != "" {
		sql += " ORDER BY " + order
	}
	sql += " LIMIT " + itoa(limit)
	rows, done, err := s.queryRead(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer done()
	budget := storage.NewResponseBudget(ctx)
	var out []json.RawMessage
	for rows.Next() {
		var doc []byte
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		if err := budget.Add(len(doc)); err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, rows.Err()
}
