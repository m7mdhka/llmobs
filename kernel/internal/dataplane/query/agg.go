package query

import (
	"strconv"
	"strings"
	"time"
)

// Aggregation ceilings (00-dsl-spec.md §5, §12).
const (
	maxAggregations = 10
	maxGroupBy      = 3
)

// CompiledAgg is an aggregation query compiled to SQL fragments; the adapter
// supplies the FROM source (base table for spans/scores, the synthesized
// projection for traces), mirroring the row-query split.
type CompiledAgg struct {
	Select  string // "date_bin(...) AS bucket, COUNT(*) AS c, ..."
	GroupBy string // comma-joined group aliases, or "" for a global aggregate
	Where   string
	Args    []any
}

var validAggOps = map[string]bool{
	"count": true, "count_distinct": true, "sum": true, "avg": true,
	"min": true, "max": true, "p50": true, "p90": true, "p95": true, "p99": true,
}

var bucketIntervals = map[string]string{"1m": "1 minute", "5m": "5 minutes", "1h": "1 hour", "1d": "1 day"}

// CompileAggregation compiles an aggregation query for one target (Postgres dialect).
func CompileAggregation(doc map[string]any, projectID string, maxWindow time.Duration, target string) (*CompiledAgg, error) {
	return CompileAggregationForDialect(doc, projectID, maxWindow, target, PostgresDialect)
}

// CompileAggregationForDialect compiles an aggregation query for the given dialect.
func CompileAggregationForDialect(doc map[string]any, projectID string, maxWindow time.Duration, target string, d Dialect) (*CompiledAgg, error) {
	qf, err := fieldsForTarget(target)
	if err != nil {
		return nil, err
	}
	for k := range doc {
		if !allowedTopKeys[k] {
			return nil, errf("schema_invalid", 400, "unknown top-level key %q", k)
		}
	}
	if _, bad := doc["orderBy"]; bad {
		return nil, errf("schema_invalid", 400, "orderBy is not allowed on an aggregation query")
	}
	if _, bad := doc["cursor"]; bad {
		return nil, errf("schema_invalid", 400, "cursor is not allowed on an aggregation query")
	}

	b := &builder{d: d}
	preds := []string{"project_id = " + b.ph(projectID)}
	anchor := qf.timeAnchor

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
	preds = append(preds, anchor+" >= "+b.ph(from)+" AND "+anchor+" < "+b.ph(to))

	condCount := 0
	if raw, ok := doc["filters"].([]any); ok {
		for _, m := range raw {
			member, ok := m.(map[string]any)
			if !ok {
				return nil, errf("schema_invalid", 400, "filter must be an object")
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

	selects, groupAliases, err := compileGroupBy(doc, qf, anchor, d)
	if err != nil {
		return nil, err
	}
	aggSelects, err := compileAggregations(doc, qf, d)
	if err != nil {
		return nil, err
	}
	selects = append(selects, aggSelects...)

	return &CompiledAgg{
		Select:  strings.Join(selects, ", "),
		GroupBy: strings.Join(groupAliases, ", "),
		Where:   strings.Join(preds, " AND "),
		Args:    b.args,
	}, nil
}

func fieldsForTarget(target string) (queryFields, error) {
	switch target {
	case "spans":
		return spanFields, nil
	case "traces":
		return traceFields, nil
	case "scores":
		return scoreFields, nil
	}
	return queryFields{}, errf("schema_invalid", 400, "unknown target %q", target)
}

func compileGroupBy(doc map[string]any, qf queryFields, anchor string, d Dialect) (selects, aliases []string, err error) {
	raw, ok := doc["groupBy"].([]any)
	if !ok || len(raw) == 0 {
		return nil, nil, nil
	}
	if len(raw) > maxGroupBy {
		return nil, nil, errf("schema_invalid", 400, "more than %d groupBy members", maxGroupBy)
	}
	timeBuckets := 0
	for i, m := range raw {
		alias := "g" + strconv.Itoa(i)
		switch member := m.(type) {
		case string:
			f, known := qf.fields[member]
			if !known {
				return nil, nil, errf("unknown_field", 422, "unknown groupBy field %q", member)
			}
			if !qf.groupable[member] {
				return nil, nil, errf("not_groupable", 422, "field %q is not groupable", member)
			}
			selects = append(selects, resolveCol(d, f)+" AS "+alias)
			aliases = append(aliases, alias)
		case map[string]any:
			field, _ := member["field"].(string)
			interval, _ := member["interval"].(string)
			if field != anchor {
				return nil, nil, errf("schema_invalid", 400, "time-bucket field must be the target time anchor %q", anchor)
			}
			iv, ok := bucketIntervals[interval]
			if !ok {
				return nil, nil, errf("schema_invalid", 400, "time-bucket interval must be one of 1m|5m|1h|1d")
			}
			if timeBuckets++; timeBuckets > 1 {
				return nil, nil, errf("schema_invalid", 400, "at most one time-bucket groupBy member")
			}
			// Bucket on a fixed epoch origin (dialect-rendered).
			selects = append(selects, d.timeBucket(iv, anchor)+" AS "+alias)
			aliases = append(aliases, alias)
		default:
			return nil, nil, errf("schema_invalid", 400, "groupBy member must be a field name or a time bucket")
		}
	}
	return selects, aliases, nil
}

func compileAggregations(doc map[string]any, qf queryFields, d Dialect) ([]string, error) {
	raw, ok := doc["aggregations"].([]any)
	if !ok || len(raw) == 0 {
		return nil, errf("schema_invalid", 400, "aggregations must be a non-empty array")
	}
	if len(raw) > maxAggregations {
		return nil, errf("schema_invalid", 400, "more than %d aggregations", maxAggregations)
	}
	out := make([]string, 0, len(raw))
	for _, m := range raw {
		a, ok := m.(map[string]any)
		if !ok {
			return nil, errf("schema_invalid", 400, "aggregation must be an object")
		}
		op, _ := a["op"].(string)
		if !validAggOps[op] {
			return nil, errf("operator_not_allowed", 422, "unknown aggregation op %q", op)
		}
		field, _ := a["field"].(string)
		alias, _ := a["alias"].(string)

		expr, defAlias, err := aggExpr(op, field, a, qf, d)
		if err != nil {
			return nil, err
		}
		if alias == "" {
			alias = defAlias
		}
		out = append(out, expr+" AS "+d.quoteIdent(alias))
	}
	return out, nil
}

func aggExpr(op, field string, a map[string]any, qf queryFields, d Dialect) (expr, defAlias string, err error) {
	if op == "count" && field == "" {
		return "COUNT(*)", "count", nil
	}
	f, known := qf.fields[field]
	if !known {
		return "", "", errf("unknown_field", 422, "unknown aggregation field %q", field)
	}
	defAlias = op + "_" + field
	col := resolveCol(d, f)

	// A numeric map key (usage_details/cost_details with a key) is a numeric value.
	if f.class == classAttrMap || f.class == classNumericMap {
		key, _ := a["key"].(string)
		if key == "" {
			return "", "", errf("operator_not_allowed", 422, "aggregating %q requires a map key", field)
		}
		guarded := d.mapNumCast(f.col, key)
		defAlias = op + "_" + field + "_" + key
		return numericAgg(op, guarded, defAlias, d)
	}

	isStr := f.class == classString || f.class == classEnum
	switch op {
	case "count_distinct":
		return d.countExpr(col, true, isStr), "count_distinct_" + field, nil
	case "count":
		return d.countExpr(col, false, isStr), "count_" + field, nil
	case "sum", "avg", "min", "max", "p50", "p90", "p95", "p99":
		if f.class != classNumeric {
			return "", "", errf("operator_not_allowed", 422, "%s requires a numeric field, got %q", op, field)
		}
		return numericAgg(op, col, defAlias, d)
	}
	return "", "", errf("operator_not_allowed", 422, "op %q not allowed", op)
}

// numericAgg emits the dialect's numeric aggregate. Numeric results are rendered
// as a clean float so the driver returns float64 (COUNT stays integer).
func numericAgg(op, col, defAlias string, d Dialect) (string, string, error) {
	expr, ok := d.numericAggExpr(op, col)
	if !ok {
		return "", "", errf("operator_not_allowed", 422, "op %q not allowed on numeric", op)
	}
	return expr, defAlias, nil
}
