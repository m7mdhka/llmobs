package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// ReadLimits are the mandatory per-query ClickHouse resource caps (RULING-CH9).
// Every DSL read carries all four; the adapter refuses to emit a read until they
// are set to fully-valid values — fail-closed, so a pathological query is capped,
// never able to take the cluster down.
type ReadLimits struct {
	MaxExecutionTime time.Duration // wall-clock cap (→ max_execution_time seconds)
	MaxMemoryUsage   uint64        // bytes a single query may allocate
	MaxRowsToRead    uint64        // rows a single query may scan
	MaxBytesToRead   uint64        // bytes a single query may scan
}

func (l ReadLimits) valid() bool {
	return l.MaxExecutionTime > 0 && l.MaxMemoryUsage > 0 && l.MaxRowsToRead > 0 && l.MaxBytesToRead > 0
}

// settings renders the SETTINGS clause. Values are operator-configured integers
// (never user data), so inlining them is safe. max_execution_time is whole
// seconds (ClickHouse accepts fractional, but our floor is coarse).
func (l ReadLimits) settings() string {
	return fmt.Sprintf(
		" SETTINGS max_execution_time=%d, max_memory_usage=%d, max_rows_to_read=%d, max_bytes_to_read=%d",
		int64(l.MaxExecutionTime.Seconds()), l.MaxMemoryUsage, l.MaxRowsToRead, l.MaxBytesToRead)
}

// errReadLimitsUnset is the fail-closed refusal when resource caps are missing.
var errReadLimitsUnset = fmt.Errorf(
	"clickhouse read refused: per-query resource limits not configured (RULING-CH9 — set all of " +
		"max_execution_time/max_memory_usage/max_rows_to_read/max_bytes_to_read)")

// readGuard returns the SETTINGS clause for a read, or the fail-closed error. It is
// the SINGLE seam through which every DSL read (queryDocs, getDoc, GetTraceSpans,
// QueryTraces, QueryAggregation) and the erasure id-resolver obtain their settings, so
// the erasure read-back guarantees below apply to EVERY read by construction — a new
// read path inherits them by calling readGuard, not by remembering to add settings
// (invariant 11).
//
// #88 (GDPR erasure read-back): apply_deleted_mask=1 is pinned on every read so a
// lightweight-deleted (erased) row is NEVER returned, regardless of the server default
// — the version floor (probeServer) guarantees the server honors it. When the server
// has the lazy-materialization optimization, it is disabled on reads too, since a plan
// reorder could otherwise surface an erased row past the mask.
func (s *Store) readGuard() (string, error) {
	if !s.limits.valid() {
		return "", errReadLimitsUnset
	}
	settings := s.limits.settings() + ", apply_deleted_mask=1"
	if s.guardLazyMaterialization {
		settings += ", query_plan_optimize_lazy_materialization=0"
	}
	return settings, nil
}

// dedupInner wraps a base table in the settled-row read model: pick the latest
// write per (project_id, id) by `ver`, scoped to the tenant (project_id is frozen,
// so tenant pushdown is correctness-safe and bounds the scan). is_deleted is
// filtered by the CALLER, after dedup — a later tombstone is the settled row and
// must exclude the key, never fall back to an older non-deleted row.
func dedupInner(table string) string {
	return "(SELECT * FROM " + table + " WHERE project_id = ? ORDER BY ver DESC LIMIT 1 BY project_id, id)"
}

// spansSuppressionExclusion excludes any span whose (project_id, id) carries an
// UNEXPIRED erasure-suppression tombstone (#77). The deleted mask (apply_deleted_mask)
// hides a lightweight-deleted row, but PersistSpan's suppression check is not atomic
// with its insert — a concurrent write (e.g. a backfill copying an erased id) can insert
// a FRESH, non-lightweight-deleted row for an erased id AFTER the erase deleted, which
// the mask does not hide. This read-side exclusion is the other half of the guarantee:
// a resurrected row is never RETURNED while its tombstone is unexpired, for EVERY
// interleaving. Server-time now64(6) (never event-controlled) bounds it to the tombstone
// TTL, so an id legitimately re-created after the retention window reads again. The
// tuple match needs NO bind, so it appends to any spans read without disturbing arg order.
// Applied to SPANS reads only — scores are a separate id namespace and are never erased.
//
// EVERY spans-read site MUST carry this (the query shapes differ, so it is placed
// per-site, not at one seam — a new spans read must add it by this checklist):
//  1. QuerySpans/QueryScores → queryDocs (spans only)
//  2. GetSpan/GetScore → getDoc (spans only)
//  3. GetTraceSpans
//  4. the trace projection's span_base CTE (chTraceProjection)
//  5. QueryAggregation's spans target
const spansSuppressionExclusion = " AND (project_id, id) NOT IN " +
	"(SELECT project_id, id FROM erasure_suppression WHERE expires_at > now64(6))"

// projectArg extracts the tenant id the compiler always binds first (project
// scoping is predicate #1, arg #0). The dedup subquery reuses it for tenant
// pushdown, so the full bind list is [projectID] ++ compiledArgs.
func projectArg(args []any) (string, []any, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("compiled query missing project scoping arg")
	}
	pid, ok := args[0].(string)
	if !ok {
		return "", nil, fmt.Errorf("project scoping arg is not a string")
	}
	return pid, append([]any{pid}, args...), nil
}

// QuerySpans / QueryScores share the settled-row doc read.
func (s *Store) QuerySpans(ctx context.Context, where string, args []any, order string, limit int) ([]json.RawMessage, error) {
	return s.queryDocs(ctx, "spans", where, args, order, limit)
}

func (s *Store) QueryScores(ctx context.Context, where string, args []any, order string, limit int) ([]json.RawMessage, error) {
	return s.queryDocs(ctx, "scores", where, args, order, limit)
}

func (s *Store) queryDocs(ctx context.Context, table, where string, args []any, order string, limit int) ([]json.RawMessage, error) {
	settings, err := s.readGuard()
	if err != nil {
		return nil, err
	}
	pid, bind, err := projectArg(args)
	if err != nil {
		return nil, err
	}
	_ = pid
	sql := "SELECT doc FROM " + dedupInner(table) + " WHERE is_deleted = 0"
	if table == "spans" {
		sql += spansSuppressionExclusion
	}
	if where != "" {
		sql += " AND (" + where + ")"
	}
	if order != "" {
		sql += " ORDER BY " + order
	}
	sql += " LIMIT " + strconv.Itoa(limit) + settings
	return s.scanDocs(ctx, sql, bind)
}

func (s *Store) scanDocs(ctx context.Context, sql string, bind []any) ([]json.RawMessage, error) {
	rows, err := s.conn.Query(ctx, sql, bind...)
	if err != nil {
		return nil, fmt.Errorf("clickhouse read: %w", err)
	}
	defer rows.Close()
	budget := storage.NewResponseBudget(ctx)
	var out []json.RawMessage
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, fmt.Errorf("scan doc: %w", err)
		}
		if err := budget.Add(len(doc)); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(doc))
	}
	return out, rows.Err()
}

// GetSpan / GetScore return one settled, non-deleted doc by id.
func (s *Store) GetSpan(ctx context.Context, projectID, id string) (json.RawMessage, error) {
	return s.getDoc(ctx, "spans", projectID, id)
}

func (s *Store) GetScore(ctx context.Context, projectID, id string) (json.RawMessage, error) {
	return s.getDoc(ctx, "scores", projectID, id)
}

func (s *Store) getDoc(ctx context.Context, table, projectID, id string) (json.RawMessage, error) {
	settings, err := s.readGuard()
	if err != nil {
		return nil, err
	}
	// Point read of the settled row; is_deleted filtered after dedup.
	suppress := ""
	if table == "spans" {
		suppress = spansSuppressionExclusion
	}
	sql := "SELECT doc FROM (SELECT * FROM " + table +
		" WHERE project_id = ? AND id = ? ORDER BY ver DESC LIMIT 1 BY project_id, id) WHERE is_deleted = 0" + suppress + settings
	rows, err := s.conn.Query(ctx, sql, projectID, id)
	if err != nil {
		return nil, fmt.Errorf("clickhouse get: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, fmt.Errorf("scan doc: %w", err)
		}
		return json.RawMessage(doc), rows.Err()
	}
	return nil, rows.Err()
}

// GetTraceSpans returns a trace's settled non-deleted spans in tree-buildable
// order (start_time, id) — matching the lite adapter.
func (s *Store) GetTraceSpans(ctx context.Context, projectID, traceID string) ([]json.RawMessage, error) {
	settings, err := s.readGuard()
	if err != nil {
		return nil, err
	}
	// trace_id is a frozen field, so pushing it into the dedup subquery is
	// correctness-safe (every version of a span shares its trace_id) AND bounds the
	// scan to one trace instead of the whole project.
	sql := "SELECT doc FROM (SELECT * FROM spans WHERE project_id = ? AND trace_id = ? ORDER BY ver DESC LIMIT 1 BY project_id, id) " +
		"WHERE is_deleted = 0" + spansSuppressionExclusion + " ORDER BY start_time ASC, id ASC" + settings
	rows, err := s.conn.Query(ctx, sql, projectID, traceID)
	if err != nil {
		return nil, fmt.Errorf("clickhouse get trace spans: %w", err)
	}
	defer rows.Close()
	budget := storage.NewResponseBudget(ctx)
	var out []json.RawMessage
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, fmt.Errorf("scan doc: %w", err)
		}
		if err := budget.Add(len(doc)); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(doc))
	}
	return out, rows.Err()
}

// chTraceProjection synthesizes one row per trace from its settled spans (DSL
// §4.1), mirroring the lite adapter's projection with ClickHouse idioms:
//   - DISTINCT ON (trace_id) → ORDER BY … LIMIT 1 BY trace_id
//   - bool_or → max() over the UInt8 predicate
//   - the orphan-with-parent-ref incomplete_trace, without a correlated subquery:
//     collect each trace's id set and referenced parents as arrays, then test
//     arrayExists(parent ∉ ids). The single `?` is the tenant scope for span_base.
var chTraceProjection = `
WITH span_base AS (
    SELECT * FROM (
        SELECT * FROM spans WHERE project_id = ? ORDER BY ver DESC LIMIT 1 BY project_id, id
    ) WHERE is_deleted = 0` + spansSuppressionExclusion + `
),
roots AS (
    SELECT trace_id, name, environment, release, version, session_id, user_id, status_code, attributes
    FROM span_base
    ORDER BY trace_id, (parent_span_id != '') ASC, start_time ASC, id ASC
    LIMIT 1 BY trace_id
),
agg AS (
    -- Aggregate aliases are deliberately NOT the base column names: aliasing
    -- max(end_time) AS end_time would make coalesce(max(end_time), …) resolve to
    -- max(max(end_time)) (nested-aggregate error). Distinct names avoid the shadow.
    SELECT trace_id, project_id,
        min(start_time) AS t_start,
        max(end_time)   AS t_end,
        max(status_code = 'error') AS any_error,
        max(end_time IS NULL) AS is_open,
        coalesce(max(end_time), max(start_time)) AS t_last,
        groupArray(id) AS ids,
        groupArrayIf(parent_span_id, parent_span_id != '') AS parents,
        -- trace-level cost (§7.1): sum NON-aggregate spans' cost; the countIf guard
        -- reproduces the lite adapter's NULL-when-no-leaf-cost (ClickHouse sum returns 0
        -- for no matching rows, Postgres SUM…FILTER returns NULL — this bridges them).
        sumIf(total_cost, kind NOT IN (` + storage.AggregateKindsSQL() + `)) AS t_cost_sum,
        countIf(total_cost IS NOT NULL AND kind NOT IN (` + storage.AggregateKindsSQL() + `)) AS t_cost_n,
        count() AS span_count
    FROM span_base
    GROUP BY trace_id, project_id
),
trace_proj AS (
    SELECT
        a.trace_id AS id, a.project_id AS project_id, a.t_start AS start_time,
        a.t_end AS end_time, a.t_last AS last_activity,
        if(a.any_error, 'error', r.status_code) AS status_code,
        r.name AS name, r.environment AS environment, r.release AS release, r.version AS version,
        r.session_id AS session_id, r.user_id AS user_id, r.attributes AS attributes,
        a.span_count AS span_count, a.is_open AS is_open,
        arrayExists(p -> not has(a.ids, p), a.parents) AS incomplete_trace,
        -- round to the shared scale (storage.CostRoundDecimals) so the float64 sum is
        -- byte-identical to Postgres's exact-NUMERIC sum; NULL when no leaf cost.
        if(a.t_cost_n = 0, NULL, round(a.t_cost_sum, ` + storage.CostRoundSQL() + `)) AS total_cost
    FROM agg a INNER JOIN roots r ON r.trace_id = a.trace_id
)`

// QueryTraces runs a compiled traces query against the synthesized projection and
// builds the SAME trace docs the lite adapter builds (byte-identical output is the
// cross-adapter contract).
func (s *Store) QueryTraces(ctx context.Context, where string, args []any, order string, limit int) ([]json.RawMessage, error) {
	settings, err := s.readGuard()
	if err != nil {
		return nil, err
	}
	_, bind, err := projectArg(args)
	if err != nil {
		return nil, err
	}
	sql := chTraceProjection + `
SELECT id, project_id, start_time, end_time, last_activity, status_code, name,
       environment, release, version, session_id, user_id, attributes, span_count, is_open, incomplete_trace, total_cost
FROM trace_proj`
	if where != "" {
		sql += " WHERE " + where
	}
	if order != "" {
		sql += " ORDER BY " + order
	}
	sql += " LIMIT " + strconv.Itoa(limit) + settings

	rows, err := s.conn.Query(ctx, sql, bind...)
	if err != nil {
		return nil, fmt.Errorf("clickhouse query traces: %w", err)
	}
	defer rows.Close()
	budget := storage.NewResponseBudget(ctx)
	var out []json.RawMessage
	for rows.Next() {
		var (
			id, projectID, statusCode, name, environment, release, version, sessionID, userID, attributes string
			startTime                                                                                     time.Time
			endTime, lastActivity                                                                         *time.Time
			spanCount                                                                                     uint64
			isOpenU8, incompleteU8                                                                        uint8
			totalCost                                                                                     *float64
		)
		if err := rows.Scan(&id, &projectID, &startTime, &endTime, &lastActivity, &statusCode, &name,
			&environment, &release, &version, &sessionID, &userID, &attributes, &spanCount, &isOpenU8, &incompleteU8, &totalCost); err != nil {
			return nil, fmt.Errorf("scan trace row: %w", err)
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
			"span_count":  int64(spanCount),
			"is_open":     isOpenU8 != 0,
		}
		if incompleteU8 != 0 {
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
		// Match the lite adapter exactly: include attributes whenever the column
		// parses, even when it is an empty object (lite emits "attributes":{}).
		if attributes != "" {
			var attrs map[string]any
			if json.Unmarshal([]byte(attributes), &attrs) == nil {
				doc["attributes"] = attrs
			}
		}
		b, err := json.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("marshal trace doc: %w", err)
		}
		if err := budget.Add(len(b)); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// QueryAggregation runs a compiled aggregation. FROM source per target mirrors
// the lite adapter (settled base for spans/scores; the trace projection for
// traces); results are read generically into column→value maps.
func (s *Store) QueryAggregation(ctx context.Context, target, sel, where, groupBy string, args []any) ([]map[string]any, error) {
	settings, err := s.readGuard()
	if err != nil {
		return nil, err
	}
	_, bind, err := projectArg(args)
	if err != nil {
		return nil, err
	}

	var sql string
	switch target {
	case "spans", "scores":
		sql = "SELECT " + sel + " FROM " + dedupInner(target) + " WHERE is_deleted = 0"
		if target == "spans" {
			// #77: the aggregation spans target is a spans read too — it must carry the
			// suppression exclusion, or a resurrected erased span would still be counted/
			// summed/grouped (an aggregate-level erasure leak).
			sql += spansSuppressionExclusion
		}
		if where != "" {
			sql += " AND (" + where + ")"
		}
	case "traces":
		sql = chTraceProjection + " SELECT " + sel + " FROM trace_proj"
		if where != "" {
			sql += " WHERE " + where
		}
	default:
		return nil, fmt.Errorf("unknown aggregation target %q", target)
	}
	if groupBy != "" {
		sql += " GROUP BY " + groupBy
	}
	sql += " LIMIT 10000" + settings

	rows, err := s.conn.Query(ctx, sql, bind...)
	if err != nil {
		return nil, fmt.Errorf("clickhouse aggregation: %w", err)
	}
	defer rows.Close()

	cols := rows.Columns()
	cts := rows.ColumnTypes()
	var out []map[string]any
	for rows.Next() {
		holders := make([]any, len(cts))
		for i, ct := range cts {
			holders[i] = reflect.New(ct.ScanType()).Interface()
		}
		if err := rows.Scan(holders...); err != nil {
			return nil, fmt.Errorf("scan aggregation row: %w", err)
		}
		m := make(map[string]any, len(cols))
		for i, name := range cols {
			m[name] = normalizeCHAggValue(reflect.ValueOf(holders[i]).Elem().Interface())
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// normalizeCHAggValue makes a scanned ClickHouse aggregate value JSON-comparable
// with the lite adapter's output: deref Nullable pointers, normalize ” group
// values to nil (the canonical model treats absent == ” == null, so a group key
// on an unset dimension must serialize as null, matching Postgres NULL), and
// present times in UTC.
func normalizeCHAggValue(v any) any {
	switch t := v.(type) {
	case *string:
		if t == nil || *t == "" {
			return nil
		}
		return *t
	case string:
		if t == "" {
			return nil
		}
		return t
	case *float64:
		if t == nil {
			return nil
		}
		return *t
	case *time.Time:
		if t == nil {
			return nil
		}
		return t.UTC()
	case time.Time:
		return t.UTC()
	case uint8:
		// Boolean group dimensions (is_open, incomplete_trace) are computed as
		// UInt8 in the trace projection; the lite adapter returns a real bool. Map
		// back so a group-by on a boolean dimension serializes as true/false, not 1/0.
		return t != 0
	case *uint8:
		if t == nil {
			return nil
		}
		return *t != 0
	default:
		return t
	}
}
