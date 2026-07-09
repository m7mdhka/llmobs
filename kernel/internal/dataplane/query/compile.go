// Package query compiles the typed DSL (api/query/v1alpha1) into parameterized
// SQL per storage adapter (D9 — the ClickHouse compiler is a future sibling behind
// the same interface). v1alpha1/B1 implements the `spans` target: filters
// (simple, attr_map, reference classes), an OR group, mandatory timeRange,
// keyset pagination, and the contract ceilings + 422 taxonomy.
package query

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Ceilings (00-dsl-spec.md §12).
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
)

// spanFields maps queryable span fields to (column, class). Mirrors fields.json;
// a CI check keeps fields.json in sync with the model (unknown fields -> 422).
var spanFields = map[string]struct {
	col   string
	class fieldClass
}{
	"id":                   {"id", classString},
	"trace_id":             {"trace_id", classString},
	"parent_span_id":       {"parent_span_id", classString},
	"kind":                 {"kind", classEnum},
	"raw_kind":             {"raw_kind", classString},
	"name":                 {"name", classString},
	"start_time":           {"start_time", classTimestamp},
	"end_time":             {"end_time", classTimestamp},
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
}

var orderableFields = map[string]bool{
	"id": true, "trace_id": true, "name": true, "start_time": true, "end_time": true, "total_cost": true,
}

type builder struct {
	args []any
}

func (b *builder) ph(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
}

// CompileSpans compiles a spans query for the given project.
func CompileSpans(doc map[string]any, projectID string, maxWindow time.Duration) (*Compiled, error) {
	if t, _ := doc["target"].(string); t != "spans" {
		return nil, errf("schema_invalid", 400, "target must be 'spans'")
	}
	b := &builder{}
	// project scoping is always first.
	preds := []string{"project_id = " + b.ph(projectID)}

	// timeRange (mandatory, QD-5)
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
	preds = append(preds, "start_time >= "+b.ph(from)+" AND start_time < "+b.ph(to))

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
					sql, err := compileCondition(b, cm)
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
			sql, err := compileCondition(b, member)
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

	order, err := compileOrder(doc)
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

	// keyset cursor (default order only in B1)
	if cur, ok := doc["cursor"].(string); ok && cur != "" {
		st, id, err := decodeCursor(cur)
		if err != nil {
			return nil, errf("schema_invalid", 400, "invalid cursor")
		}
		preds = append(preds, "(start_time < "+b.ph(st)+" OR (start_time = "+b.ph(st)+" AND id > "+b.ph(id)+"))")
	}

	return &Compiled{Where: strings.Join(preds, " AND "), Args: b.args, Order: order, Limit: limit}, nil
}

func compileCondition(b *builder, c map[string]any) (string, error) {
	field, _ := c["field"].(string)
	op, _ := c["op"].(string)
	f, known := spanFields[field]
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

func compileSimpleCond(b *builder, f struct {
	col   string
	class fieldClass
}, c map[string]any, op string) (string, error) {
	col := f.col
	num := f.class == classNumeric || f.class == classTimestamp
	switch op {
	case "eq":
		return col + " = " + b.ph(coerce(f, c["value"])), nil
	case "neq":
		return col + " IS DISTINCT FROM " + b.ph(coerce(f, c["value"])), nil
	case "is_null":
		return col + " IS NULL", nil
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
		expr := col + " = ANY(" + b.ph(vals) + ")"
		if op == "not_in" {
			return "NOT (" + expr + ")", nil
		}
		return expr, nil
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
	jsonPath := col + " ->> " + b.ph(key)
	switch op {
	case "exists":
		return col + " ? " + b.ph(key), nil
	case "eq":
		if numeric {
			return "(" + jsonPath + ")::numeric = " + b.ph(asNumber(c["value"])), nil
		}
		return jsonPath + " = " + b.ph(asString(c["value"])), nil
	case "neq":
		if numeric {
			return "(" + jsonPath + ")::numeric IS DISTINCT FROM " + b.ph(asNumber(c["value"])), nil
		}
		return jsonPath + " IS DISTINCT FROM " + b.ph(asString(c["value"])), nil
	case "contains":
		if numeric {
			return "", errf("operator_not_allowed", 422, "contains only on string map")
		}
		return jsonPath + " LIKE " + b.ph("%"+asString(c["value"])+"%"), nil
	case "gt", "gte", "lt", "lte":
		if !numeric {
			return "", errf("operator_not_allowed", 422, "%s only on numeric map", op)
		}
		return "(" + jsonPath + ")::numeric " + sqlCmp(op) + " " + b.ph(asNumber(c["value"])), nil
	default:
		return "", errf("operator_not_allowed", 422, "map operator %q not allowed", op)
	}
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
	pred := col + " ->> 'ref_type' = " + b.ph(rt) + " AND " + col + " ->> 'ref_id' = " + b.ph(ri)
	if rl, ok := v["ref_label"].(string); ok && rl != "" {
		pred += " AND " + col + " ->> 'ref_label' = " + b.ph(rl)
	}
	return "(" + pred + ")", nil
}

func compileOrder(doc map[string]any) (string, error) {
	if raw, ok := doc["orderBy"].([]any); ok && len(raw) > 0 {
		var parts []string
		for _, m := range raw {
			o, _ := m.(map[string]any)
			field, _ := o["field"].(string)
			dir, _ := o["dir"].(string)
			f, known := spanFields[field]
			if !known {
				return "", errf("unknown_field", 422, "unknown orderBy field %q", field)
			}
			if !orderableFields[field] {
				return "", errf("not_orderable", 422, "field %q is not orderable", field)
			}
			d := "ASC"
			if strings.ToLower(dir) == "desc" {
				d = "DESC"
			}
			parts = append(parts, f.col+" "+d)
		}
		parts = append(parts, "id ASC")
		return strings.Join(parts, ", "), nil
	}
	return "start_time DESC, id ASC", nil
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

func coerce(f struct {
	col   string
	class fieldClass
}, v any) any {
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

// Cursor: base64 of "<rfc3339nano>|<id>".
func EncodeCursor(startTime time.Time, id string) string {
	return base64.StdEncoding.EncodeToString([]byte(startTime.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeCursor(s string) (time.Time, string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, "", err
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("bad cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	return t, parts[1], err
}

// jsonBody decodes a request body into a query doc.
func jsonBody(b []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}
