// Package query compiles the typed DSL (api/query/v1alpha1) into parameterized
// SQL per storage adapter — the DSL is the only read path over the canonical
// model, with no raw-SQL escape hatch, and each adapter (Postgres, ClickHouse)
// is a sibling behind the same Dialect interface. The compiler implements the
// `spans` target: filters (simple, attr_map, reference classes), an OR group,
// mandatory timeRange, keyset pagination, and the contract ceilings + 422 taxonomy.
package query

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Contract ceilings, enforced before execution (versioned DSL constants):
// max total conditions per query, max/default row-query page size, and max
// elements in an in/not_in list.
const (
	maxConditions = 32
	maxLimit      = 1000
	maxInList     = 256
)

// CompileError carries the machine-readable error.code and HTTP status.
type CompileError struct {
	Code   string
	Status int
	Msg    string
}

func (e *CompileError) Error() string { return e.Code + ": " + e.Msg }

func errf(code string, status int, format string, a ...any) *CompileError {
	return &CompileError{Code: code, Status: status, Msg: fmt.Sprintf(format, a...)}
}

// Compiled is the result: a WHERE predicate (with project scoping), ordered args,
// an ORDER BY clause, and the effective limit.
type Compiled struct {
	Where string
	Args  []any
	Order string
	Limit int
	// Fingerprint binds a keyset cursor to the query shape (filters, timeRange,
	// orderBy). The next page's cursor must carry it; a cursor from a different
	// query shape is rejected.
	Fingerprint string
	// OrderKeys is the LOGICAL order (field names + direction) the Order SQL encodes,
	// dialect-neutral. The dual-read merge sorts the lite∪scale union by these keys —
	// it cannot parse the dialect-specific Order SQL (a computed field like `duration`
	// resolves to an engine expression, not a doc field). Computed fields (duration,
	// ttft) are recomputed from doc timestamps by the merge.
	OrderKeys []OrderKey
}

// OrderKey is one logical ORDER BY term: a canonical field name (e.g. "start_time",
// "duration") and its direction. The trailing "id ASC" tiebreak is always included.
type OrderKey struct {
	Field string
	Desc  bool
}

type fieldClass int

const (
	classString fieldClass = iota
	classEnum
	classNumeric
	classTimestamp
	classAttrMap
	classNumericMap
	classReference
	classBoolean
)

type fieldDef struct {
	col   string
	class fieldClass
}

// queryFields is the per-target queryable surface: the field→(column,class) map
// and the orderable subset. Mirrors fields.json; a CI check keeps fields.json in
// sync with the model (unknown fields -> 422).
type queryFields struct {
	fields    map[string]fieldDef
	orderable map[string]bool
	groupable map[string]bool
	// timeAnchor is the column the mandatory timeRange, keyset cursor, and default
	// order key are anchored on (spans/traces: start_time; scores: timestamp).
	timeAnchor string
}

var spanFields = queryFields{
	fields: map[string]fieldDef{
		"id":                    {"id", classString},
		"trace_id":              {"trace_id", classString},
		"parent_span_id":        {"parent_span_id", classString},
		"kind":                  {"kind", classEnum},
		"raw_kind":              {"raw_kind", classString},
		"name":                  {"name", classString},
		"start_time":            {"start_time", classTimestamp},
		"end_time":              {"end_time", classTimestamp},
		"completion_start_time": {"completion_start_time", classTimestamp},
		// Computed numeric fields: resolved per-dialect to a
		// time-difference expression in fractional seconds (resolveCol /
		// Dialect.computedCol). NULL when an endpoint is null (open span / no TTFT).
		"duration":             {"duration", classNumeric},
		"ttft":                 {"ttft", classNumeric},
		"status.code":          {"status_code", classEnum},
		"environment":          {"environment", classString},
		"release":              {"release", classString},
		"version":              {"version", classString},
		"session_id":           {"session_id", classString},
		"user_id":              {"user_id", classString},
		"model":                {"model", classString},
		"provider":             {"provider", classString},
		"total_cost":           {"total_cost", classNumeric},
		"attributes":           {"attributes", classAttrMap},
		"usage_details":        {"usage_details", classNumericMap},
		"cost_details":         {"cost_details", classNumericMap},
		"prompt_ref":           {"prompt_ref", classReference},
		"pricing_snapshot_ref": {"pricing_snapshot_ref", classReference},
	},
	orderable: map[string]bool{
		"id": true, "trace_id": true, "name": true, "start_time": true, "end_time": true,
		"completion_start_time": true, "duration": true, "ttft": true, "total_cost": true,
	},
	groupable: map[string]bool{
		"kind": true, "raw_kind": true, "name": true, "start_time": true, "status.code": true,
		"environment": true, "release": true, "version": true, "session_id": true, "user_id": true,
		"model": true, "provider": true,
	},
	timeAnchor: "start_time",
}

// traceFields is the queryable surface of the derived `traces` target. Columns
// reference the synthesized trace projection (see postgres.QueryTraces): each
// value is derived from the trace's spans (start_time = the minimum span
// start_time, end_time = the maximum span end_time, the dimensional fields taken
// from the root span, status = error if any span errored). `tags` (string_array)
// is in fields.json but the compiler has no string_array support yet, so
// filtering on it is deferred.
var traceFields = queryFields{
	fields: map[string]fieldDef{
		"id":          {"id", classString},
		"name":        {"name", classString},
		"start_time":  {"start_time", classTimestamp},
		"end_time":    {"end_time", classTimestamp},
		"status.code": {"status_code", classEnum},
		"environment": {"environment", classString},
		"release":     {"release", classString},
		"version":     {"version", classString},
		"session_id":  {"session_id", classString},
		"user_id":     {"user_id", classString},
		"attributes":  {"attributes", classAttrMap},
		// Activity anchor + incompleteness: "active runs regardless of age".
		"last_activity":    {"last_activity", classTimestamp},
		"is_open":          {"is_open", classBoolean},
		"incomplete_trace": {"incomplete_trace", classBoolean},
		// Derived trace-level cost: SUM of non-aggregate spans' total_cost.
		"total_cost": {"total_cost", classNumeric},
	},
	orderable: map[string]bool{
		"id": true, "name": true, "start_time": true, "end_time": true, "last_activity": true,
		"total_cost": true,
	},
	groupable: map[string]bool{
		"name": true, "start_time": true, "status.code": true, "environment": true, "release": true,
		"version": true, "session_id": true, "user_id": true, "is_open": true, "incomplete_trace": true,
	},
	timeAnchor: "start_time",
}

// scoreFields is the queryable surface of the `scores` target (per fields.json).
// Time anchor is `timestamp`.
var scoreFields = queryFields{
	fields: map[string]fieldDef{
		"id":            {"id", classString},
		"subject_type":  {"subject_type", classString},
		"subject_id":    {"subject_id", classString},
		"name":          {"name", classString},
		"data_type":     {"data_type", classEnum},
		"value_numeric": {"value_numeric", classNumeric},
		"value_string":  {"value_string", classString},
		"source":        {"source", classEnum},
		"timestamp":     {"timestamp", classTimestamp},
		"environment":   {"environment", classString},
		"comment":       {"comment", classString},
		"metadata":      {"metadata", classAttrMap},
		"config_ref":    {"config_ref", classReference},
	},
	orderable: map[string]bool{
		"id": true, "name": true, "value_numeric": true, "timestamp": true,
	},
	groupable: map[string]bool{
		"subject_type": true, "name": true, "data_type": true, "value_string": true,
		"source": true, "timestamp": true, "environment": true,
	},
	timeAnchor: "timestamp",
}

type builder struct {
	args []any
	d    Dialect
}

func (b *builder) ph(v any) string {
	b.args = append(b.args, v)
	return b.d.placeholder(len(b.args))
}

// resolveCol maps a fieldDef to its SQL column expression: a computed field
// (duration, ttft) resolves to the dialect's time-difference expression; every
// other field's col is a plain identifier, dialect-neutral.
func resolveCol(d Dialect, f fieldDef) string {
	if expr, ok := d.computedCol(f.col); ok {
		return expr
	}
	return f.col
}

// allowedTopKeys is the closed set of top-level query keys. Unknown
// keys are rejected (schema_invalid) so typos never silently no-op.
var allowedTopKeys = map[string]bool{
	"version": true, "target": true, "timeRange": true, "filters": true,
	"orderBy": true, "limit": true, "cursor": true,
	"groupBy": true, "aggregations": true, "scores": true,
}

// PostgresDialect and ClickHouseDialect are the two SQL emitters the compiler can
// target. The lite server uses Postgres; the scale server uses ClickHouse. The
// no-arg Compile* helpers default to Postgres (the reference dialect); the server
// selects per its store via the *ForDialect variants.
var (
	PostgresDialect   Dialect = pgDialect{}
	ClickHouseDialect Dialect = chDialect{}
)

// CompileSpans compiles a spans query for the given project (Postgres dialect).
func CompileSpans(doc map[string]any, projectID string, maxWindow time.Duration) (*Compiled, error) {
	return CompileSpansForDialect(doc, projectID, maxWindow, PostgresDialect)
}

// CompileSpansForDialect compiles a spans query, emitting SQL for the given dialect.
func CompileSpansForDialect(doc map[string]any, projectID string, maxWindow time.Duration, d Dialect) (*Compiled, error) {
	if t, _ := doc["target"].(string); t != "spans" {
		return nil, errf("schema_invalid", 400, "target must be 'spans'")
	}
	// scoreConditions only apply to the traces target.
	if _, hasScores := doc["scores"]; hasScores {
		return nil, errf("schema_invalid", 400, "scores are only valid on the traces target")
	}
	return compileTarget(doc, projectID, maxWindow, spanFields, d)
}

// CompileTraces compiles a traces query (Postgres dialect). Trace fields are
// derived from spans; the compiled predicate runs against the synthesized trace
// projection. The `scores` semi-join is supported.
func CompileTraces(doc map[string]any, projectID string, maxWindow time.Duration) (*Compiled, error) {
	return CompileTracesForDialect(doc, projectID, maxWindow, PostgresDialect)
}

// CompileTracesForDialect compiles a traces query for the given dialect.
func CompileTracesForDialect(doc map[string]any, projectID string, maxWindow time.Duration, d Dialect) (*Compiled, error) {
	if t, _ := doc["target"].(string); t != "traces" {
		return nil, errf("schema_invalid", 400, "target must be 'traces'")
	}
	return compileTarget(doc, projectID, maxWindow, traceFields, d)
}

// CompileScores compiles a scores query (Postgres dialect). The time anchor is
// `timestamp`.
func CompileScores(doc map[string]any, projectID string, maxWindow time.Duration) (*Compiled, error) {
	return CompileScoresForDialect(doc, projectID, maxWindow, PostgresDialect)
}

// CompileScoresForDialect compiles a scores query for the given dialect.
func CompileScoresForDialect(doc map[string]any, projectID string, maxWindow time.Duration, d Dialect) (*Compiled, error) {
	if t, _ := doc["target"].(string); t != "scores" {
		return nil, errf("schema_invalid", 400, "target must be 'scores'")
	}
	if _, hasScores := doc["scores"]; hasScores {
		return nil, errf("schema_invalid", 400, "the scores semi-join block is only valid on the traces target")
	}
	return compileTarget(doc, projectID, maxWindow, scoreFields, d)
}

// compileTarget is the shared row-query compiler; qf selects the target's
// queryable fields, so spans and traces share NULL/ceiling/422/cursor semantics.
// d selects the SQL dialect emitted.
func compileTarget(doc map[string]any, projectID string, maxWindow time.Duration, qf queryFields, d Dialect) (*Compiled, error) {
	for k := range doc {
		if !allowedTopKeys[k] {
			return nil, errf("schema_invalid", 400, "unknown top-level key %q", k)
		}
	}
	b := &builder{d: d}
	// project scoping is always first.
	preds := []string{"project_id = " + b.ph(projectID)}

	// timeRange (mandatory)
	tr, ok := doc["timeRange"].(map[string]any)
	if !ok {
		return nil, errf("schema_invalid", 400, "timeRange is required")
	}
	from, ferr := parseTime(tr["from"])
	to, terr := parseTime(tr["to"])
	if ferr != nil || terr != nil {
		return nil, errf("schema_invalid", 400, "timeRange.from/to must be RFC3339")
	}
	if !from.Before(to) {
		return nil, errf("schema_invalid", 400, "timeRange.from must be before to")
	}
	if maxWindow > 0 && to.Sub(from) > maxWindow {
		return nil, errf("ceiling_exceeded", 422, "timeRange window exceeds LLMOBS_QUERY_MAX_WINDOW")
	}
	anchor := qf.timeAnchor
	preds = append(preds, anchor+" >= "+b.ph(from)+" AND "+anchor+" < "+b.ph(to))

	// filters
	condCount := 0
	if raw, ok := doc["filters"].([]any); ok {
		for _, m := range raw {
			member, ok := m.(map[string]any)
			if !ok {
				return nil, errf("schema_invalid", 400, "filter must be an object")
			}
			if anyList, isOr := member["any"].([]any); isOr {
				var ors []string
				for _, c := range anyList {
					cm, _ := c.(map[string]any)
					// MAX_NESTING_DEPTH = 2 (an AND of ORs): an OR member must be a
					// leaf condition, never another group.
					if _, nested := cm["any"]; nested {
						return nil, errf("schema_invalid", 400, "nesting exceeds MAX_NESTING_DEPTH (2)")
					}
					sql, err := compileCondition(b, cm, qf)
					if err != nil {
						return nil, err
					}
					ors = append(ors, sql)
					condCount++
				}
				if len(ors) > 0 {
					preds = append(preds, "("+strings.Join(ors, " OR ")+")")
				}
				continue
			}
			sql, err := compileCondition(b, member, qf)
			if err != nil {
				return nil, err
			}
			preds = append(preds, sql)
			condCount++
		}
	}
	if condCount > maxConditions {
		return nil, errf("condition_limit_exceeded", 422, "more than %d conditions", maxConditions)
	}

	// scores semi-join — only reaches here on target=traces (spans and
	// scores reject a scores block earlier). Each entry is ANDed at the trace
	// level: for each, the trace must have ≥1 matching score.
	if raw, ok := doc["scores"].([]any); ok {
		for _, e := range raw {
			cond, ok := e.(map[string]any)
			if !ok {
				return nil, errf("schema_invalid", 400, "score condition must be an object")
			}
			pred, err := compileScoreCondition(b, projectID, cond)
			if err != nil {
				return nil, err
			}
			preds = append(preds, pred)
		}
	}

	order, orderKeys, err := compileOrder(doc, qf, d)
	if err != nil {
		return nil, err
	}
	limit := maxLimit
	if l, ok := doc["limit"]; ok {
		lv, ok := toInt(l)
		if !ok || lv < 1 || lv > maxLimit {
			return nil, errf("schema_invalid", 400, "limit must be 1..%d", maxLimit)
		}
		limit = lv
	}

	// keyset cursor (default order only). The cursor is bound to the query
	// shape so paging with a mutated query is rejected rather than silently wrong.
	fp := queryFingerprint(doc)
	if cur, ok := doc["cursor"].(string); ok && cur != "" {
		cfp, st, id, err := decodeCursor(cur)
		if err != nil {
			return nil, errf("schema_invalid", 400, "invalid cursor")
		}
		if cfp != fp {
			return nil, errf("schema_invalid", 400, "cursor does not match query (filters/timeRange/orderBy changed)")
		}
		preds = append(preds, "("+anchor+" < "+b.ph(st)+" OR ("+anchor+" = "+b.ph(st)+" AND id > "+b.ph(id)+"))")
	}

	return &Compiled{Where: strings.Join(preds, " AND "), Args: b.args, Order: order, Limit: limit, Fingerprint: fp, OrderKeys: orderKeys}, nil
}

// compileScoreCondition builds one EXISTS predicate: the trace has ≥1 score
// (subject_type='trace', subject_id=trace_proj.id) matching the condition.
// data_type fully determines the operator set, matched column, and value JSON
// type — no inference, no coercion.
func compileScoreCondition(b *builder, projectID string, c map[string]any) (string, error) {
	name, _ := c["name"].(string)
	if name == "" {
		return "", errf("schema_invalid", 400, "score condition requires name")
	}
	dataType, _ := c["data_type"].(string)
	op, _ := c["op"].(string)

	inner := []string{
		"sc.project_id = " + b.ph(projectID),
		"sc.subject_type = " + b.ph("trace"),
		"sc.subject_id = trace_proj.id",
		"sc.is_deleted = " + b.d.boolLiteral(false),
		"sc.name = " + b.ph(name),
		"sc.data_type = " + b.ph(dataType),
	}
	if src, ok := c["source"].(string); ok && src != "" {
		inner = append(inner, "sc.source = "+b.ph(src))
	}

	switch dataType {
	case "numeric":
		if !scoreNumericOp(op) {
			return "", errf("score_type_mismatch", 422, "op %q not allowed for numeric score", op)
		}
		n, ok := c["value"].(float64)
		if !ok {
			return "", errf("score_type_mismatch", 422, "numeric score value must be a number")
		}
		if op == "neq" {
			inner = append(inner, b.d.nullSafeNeq("sc.value_numeric", b.ph(n), classNumeric))
		} else {
			inner = append(inner, "sc.value_numeric "+sqlCmp(op)+" "+b.ph(n))
		}
	case "categorical":
		switch op {
		case "eq", "neq":
			s, ok := c["value"].(string)
			if !ok {
				return "", errf("score_type_mismatch", 422, "categorical score value must be a string")
			}
			if op == "neq" {
				inner = append(inner, b.d.nullSafeNeq("sc.value_string", b.ph(s), classString))
			} else {
				inner = append(inner, "sc.value_string = "+b.ph(s))
			}
		case "in":
			arr, ok := c["value"].([]any)
			if !ok {
				return "", errf("score_type_mismatch", 422, "categorical 'in' value must be an array of strings")
			}
			if len(arr) > maxInList {
				return "", errf("schema_invalid", 400, "in-list exceeds %d", maxInList)
			}
			vals := make([]any, len(arr))
			for i, v := range arr {
				s, ok := v.(string)
				if !ok {
					return "", errf("score_type_mismatch", 422, "categorical 'in' value must be an array of strings")
				}
				vals[i] = s
			}
			inner = append(inner, b.d.inArray("sc.value_string", b.ph(vals)))
		default:
			return "", errf("score_type_mismatch", 422, "op %q not allowed for categorical score", op)
		}
	case "boolean":
		if op != "eq" {
			return "", errf("score_type_mismatch", 422, "op %q not allowed for boolean score", op)
		}
		bv, ok := c["value"].(bool)
		if !ok {
			return "", errf("score_type_mismatch", 422, "boolean score value must be a boolean")
		}
		n := 0.0
		if bv {
			n = 1.0
		}
		inner = append(inner, "sc.value_numeric = "+b.ph(n))
	default:
		return "", errf("score_type_mismatch", 422, "score data_type must be numeric|categorical|boolean")
	}

	return "EXISTS (SELECT 1 FROM scores sc WHERE " + strings.Join(inner, " AND ") + ")", nil
}

func scoreNumericOp(op string) bool {
	switch op {
	case "eq", "neq", "gt", "gte", "lt", "lte":
		return true
	}
	return false
}

// queryFingerprint hashes the query shape that a cursor sequence must hold fixed:
// filters, timeRange, and orderBy (limit and cursor may vary between pages).
func queryFingerprint(doc map[string]any) string {
	shape := map[string]any{
		"filters":   doc["filters"],
		"timeRange": doc["timeRange"],
		"orderBy":   doc["orderBy"],
		"scores":    doc["scores"],
	}
	b, _ := json.Marshal(shape)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func compileCondition(b *builder, c map[string]any, qf queryFields) (string, error) {
	field, _ := c["field"].(string)
	op, _ := c["op"].(string)
	f, known := qf.fields[field]
	if !known {
		return "", errf("unknown_field", 422, "unknown field %q", field)
	}
	switch f.class {
	case classAttrMap:
		return compileMapCond(b, f.col, c, false)
	case classNumericMap:
		return compileMapCond(b, f.col, c, true)
	case classReference:
		return compileRefCond(b, f.col, c)
	default:
		return compileSimpleCond(b, f, c, op)
	}
}

func compileSimpleCond(b *builder, f fieldDef, c map[string]any, op string) (string, error) {
	col := resolveCol(b.d, f)
	num := f.class == classNumeric || f.class == classTimestamp
	if f.class == classBoolean {
		bv, ok := c["value"].(bool)
		if !ok {
			return "", errf("operator_not_allowed", 422, "boolean field requires a boolean value")
		}
		switch op {
		case "eq":
			return col + " = " + b.ph(bv), nil
		case "neq":
			return b.d.nullSafeNeq(col, b.ph(bv), f.class), nil
		default:
			return "", errf("operator_not_allowed", 422, "%s not allowed on boolean field", op)
		}
	}
	switch op {
	case "eq":
		return col + " = " + b.ph(coerce(f, c["value"])), nil
	case "neq":
		return b.d.nullSafeNeq(col, b.ph(coerce(f, c["value"])), f.class), nil
	case "is_null":
		return b.d.isNull(col, f.class), nil
	case "in", "not_in":
		arr, ok := c["value"].([]any)
		if !ok {
			return "", errf("schema_invalid", 400, "%s requires an array", op)
		}
		if len(arr) > maxInList {
			return "", errf("schema_invalid", 400, "in-list exceeds %d", maxInList)
		}
		vals := make([]any, len(arr))
		for i, v := range arr {
			vals[i] = coerce(f, v)
		}
		if op == "not_in" {
			// Uniform NULL policy: negations match unset rows.
			return b.d.notInArray(col, b.ph(vals), f.class), nil
		}
		return b.d.inArray(col, b.ph(vals)), nil
	case "gt", "gte", "lt", "lte":
		if !num {
			return "", errf("operator_not_allowed", 422, "%s not allowed on field", op)
		}
		return col + " " + sqlCmp(op) + " " + b.ph(coerce(f, c["value"])), nil
	case "contains":
		if f.class != classString {
			return "", errf("operator_not_allowed", 422, "contains only on string")
		}
		return col + " LIKE " + b.ph("%"+asString(c["value"])+"%"), nil
	case "starts_with":
		if f.class != classString {
			return "", errf("operator_not_allowed", 422, "starts_with only on string")
		}
		return col + " LIKE " + b.ph(asString(c["value"])+"%"), nil
	default:
		return "", errf("operator_not_allowed", 422, "operator %q not allowed", op)
	}
}

func compileMapCond(b *builder, col string, c map[string]any, numeric bool) (string, error) {
	key, _ := c["key"].(string)
	op, _ := c["op"].(string)
	if key == "" {
		return "", errf("schema_invalid", 400, "map condition requires key")
	}
	switch op {
	case "exists":
		return b.d.mapHasKey(col, b.ph(key)), nil
	case "eq":
		if numeric {
			return numGuard(b, col, key, "=", c["value"], false), nil
		}
		return b.d.mapExtractText(col, b.ph(key)) + " = " + b.ph(asString(c["value"])), nil
	case "neq":
		if numeric {
			// Negation: unset/non-number values match.
			return numGuard(b, col, key, b.d.numGuardNeqOp(), c["value"], true), nil
		}
		// The map key extraction is compared NULL-safely so unset keys match.
		keyExpr := b.d.mapExtractText(col, b.ph(key))
		return b.d.nullSafeNeq(keyExpr, b.ph(asString(c["value"])), classString), nil
	case "contains":
		if numeric {
			return "", errf("operator_not_allowed", 422, "contains only on string map")
		}
		return b.d.mapExtractText(col, b.ph(key)) + " LIKE " + b.ph("%"+asString(c["value"])+"%"), nil
	case "gt", "gte", "lt", "lte":
		if !numeric {
			return "", errf("operator_not_allowed", 422, "%s only on numeric map", op)
		}
		return numGuard(b, col, key, sqlCmp(op), c["value"], false), nil
	default:
		return "", errf("operator_not_allowed", 422, "map operator %q not allowed", op)
	}
}

// numGuard compiles a numeric comparison against a map value, guarding the
// ::numeric cast behind a jsonb_typeof check so non-numeric or missing values
// never raise a cast error (bad data never fails a valid query). CASE guarantees the cast is
// evaluated only when the value is a JSON number — a bare `guard AND cast`
// would let Postgres attempt the cast on other rows. unsetMatches is the ELSE
// result: true for negations (unset/non-number rows match), false otherwise.
func numGuard(b *builder, col, key, cmp string, value any, unsetMatches bool) string {
	elseVal := "false"
	if unsetMatches {
		elseVal = "true"
	}
	// The key is bound once per textual occurrence (type-guard + cast): Postgres
	// could reuse one `$N`, but ClickHouse `?` is strictly positional, so two
	// bindings keeps both dialects correct and placeholder numbering stable.
	keyPh1 := b.ph(key)
	keyPh2 := b.ph(key)
	return b.d.mapNumGuard(col, keyPh1, keyPh2, cmp, b.ph(asNumber(value)), elseVal)
}

func compileRefCond(b *builder, col string, c map[string]any) (string, error) {
	if op, _ := c["op"].(string); op != "ref_eq" {
		return "", errf("operator_not_allowed", 422, "reference field requires ref_eq")
	}
	v, _ := c["value"].(map[string]any)
	rt, _ := v["ref_type"].(string)
	ri, _ := v["ref_id"].(string)
	if rt == "" || ri == "" {
		return "", errf("schema_invalid", 400, "ref_eq requires ref_type and ref_id")
	}
	pred := b.d.refField(col, "ref_type") + " = " + b.ph(rt) + " AND " + b.d.refField(col, "ref_id") + " = " + b.ph(ri)
	if rl, ok := v["ref_label"].(string); ok && rl != "" {
		pred += " AND " + b.d.refField(col, "ref_label") + " = " + b.ph(rl)
	}
	return "(" + pred + ")", nil
}

func compileOrder(doc map[string]any, qf queryFields, d Dialect) (string, []OrderKey, error) {
	if raw, ok := doc["orderBy"].([]any); ok && len(raw) > 0 {
		var parts []string
		var keys []OrderKey
		for _, m := range raw {
			o, _ := m.(map[string]any)
			field, _ := o["field"].(string)
			dir, _ := o["dir"].(string)
			f, known := qf.fields[field]
			if !known {
				return "", nil, errf("unknown_field", 422, "unknown orderBy field %q", field)
			}
			if !qf.orderable[field] {
				return "", nil, errf("not_orderable", 422, "field %q is not orderable", field)
			}
			desc := strings.ToLower(dir) == "desc"
			dir2 := "ASC"
			if desc {
				dir2 = "DESC"
			}
			// Explicit NULLS LAST on BOTH dialects: Postgres defaults DESC→NULLS FIRST
			// while ClickHouse defaults NULLS LAST, so an unqualified DESC on a nullable/
			// computed key would place NULLs differently per engine — and the dual-read
			// merge (which sorts NULLs last) would drop scale's NULL-valued rows off a
			// full page. Forcing NULLS LAST everywhere keeps engines + merge consistent.
			parts = append(parts, resolveCol(d, f)+" "+dir2+" NULLS LAST")
			keys = append(keys, OrderKey{Field: field, Desc: desc})
		}
		parts = append(parts, "id ASC")
		keys = append(keys, OrderKey{Field: "id"})
		return strings.Join(parts, ", "), keys, nil
	}
	return qf.timeAnchor + " DESC NULLS LAST, id ASC",
		[]OrderKey{{Field: qf.timeAnchor, Desc: true}, {Field: "id"}}, nil
}

// helpers

func sqlCmp(op string) string {
	switch op {
	case "gt":
		return ">"
	case "gte":
		return ">="
	case "lt":
		return "<"
	case "lte":
		return "<="
	}
	return "="
}

func coerce(f fieldDef, v any) any {
	if f.class == classTimestamp {
		if t, err := parseTime(v); err == nil {
			return t
		}
	}
	if f.class == classNumeric {
		return asNumber(v)
	}
	return v
}

func parseTime(v any) (time.Time, error) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, fmt.Errorf("not a string")
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	return time.Parse(time.RFC3339, s)
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func asNumber(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	}
	return 0
}

func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	}
	return 0, false
}

// Cursor: base64 of "<fingerprint>|<rfc3339nano>|<id>". The fingerprint binds
// the cursor to the query shape.
func EncodeCursor(fingerprint string, startTime time.Time, id string) string {
	return base64.StdEncoding.EncodeToString(
		[]byte(fingerprint + "|" + startTime.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeCursor(s string) (fingerprint string, t time.Time, id string, err error) {
	b, derr := base64.StdEncoding.DecodeString(s)
	if derr != nil {
		return "", time.Time{}, "", derr
	}
	parts := strings.SplitN(string(b), "|", 3)
	if len(parts) != 3 {
		return "", time.Time{}, "", fmt.Errorf("bad cursor")
	}
	t, err = time.Parse(time.RFC3339Nano, parts[1])
	return parts[0], t, parts[2], err
}

// jsonBody decodes a request body into a query doc.
func jsonBody(b []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}
