package query

import (
	"strconv"
	"strings"
)

// Dialect abstracts the SQL surface that differs between storage engines, so the
// one DSL compiler (validation, ceilings, 422 taxonomy, keyset/cursor) emits
// either Postgres or ClickHouse SQL from the same parse. Everything NOT on this
// interface is dialect-neutral by construction — that is what makes cross-adapter
// conformance real: only these methods can diverge, and conformance drives both.
//
// Placeholder discipline: a dialect renders a bind placeholder for the Nth arg
// (1-based). Postgres uses `$N`; ClickHouse uses `?`. All user values are bound;
// identifiers/operators are compiler-controlled.
type Dialect interface {
	name() string
	placeholder(n int) string

	// boolLiteral renders a boolean constant in predicate position (the score
	// semi-join's `is_deleted = <false>`): Postgres `false`, ClickHouse `0`.
	boolLiteral(b bool) string

	// computedCol resolves a computed field's SQL expression (duration, ttft).
	// A logical token (§4.2) maps to the engine's time-difference expression in
	// fractional seconds, NULL when an endpoint is null/open.
	computedCol(token string) (string, bool)

	// nullSafeNeq renders `col <> value` that ALSO matches unset rows (DSL §2.2):
	// Postgres `col IS DISTINCT FROM ph`; ClickHouse depends on column nullability
	// (string columns are '' not NULL, numeric/timestamp are Nullable).
	nullSafeNeq(col, ph string, class fieldClass) string
	// isNull renders `col IS NULL` per engine (CH: `col=''` for string classes,
	// `col IS NULL` for Nullable numeric/timestamp).
	isNull(col string, class fieldClass) string
	// inArray / notInArray render `col IN (...)` / negation-matches-unset.
	inArray(col, ph string) string
	notInArray(col, ph string, class fieldClass) string

	// Map (JSON) access on a JSON-bearing column with a BOUND key placeholder.
	mapExtractText(col, keyPh string) string // ->>  / JSONExtractString
	mapHasKey(col, keyPh string) string      // ?    / JSONHas
	// mapNumGuard renders the guard-then-cast numeric comparison (§9.1): the cast
	// runs only when the value is a JSON number, else `elseVal`. The key is bound
	// TWICE (keyPh1 for the type-guard, keyPh2 for the cast) because ClickHouse `?`
	// is positional; valPh is the bound comparison value.
	mapNumGuard(col, keyPh1, keyPh2, cmp, valPh, elseVal string) string
	// numGuardNeqOp is the comparison a numeric-map neq uses inside the guarded THEN
	// (where the value is a real number): Postgres `IS DISTINCT FROM`, ClickHouse `!=`.
	numGuardNeqOp() string
	// refField renders extraction of a fixed reference subfield (`ref_type` etc.)
	// compared to a bound placeholder: `col ->> 'field'` / JSONExtractString.
	refField(col, field string) string

	// --- aggregation ---
	// timeBucket buckets the anchor to a fixed-origin interval.
	timeBucket(interval, anchor string) string
	// numericAggExpr renders sum/avg/min/max/pNN over a numeric column as a clean
	// float; count stays integer. col may itself be a guarded map-cast expression.
	numericAggExpr(op, col string) (string, bool)
	// countExpr renders count / count_distinct. For a string-class column the
	// engines must agree on "unset": Postgres stores NULL (COUNT skips it), so
	// ClickHouse — where unset is '' — must exclude '' to match. Non-string columns
	// (Nullable numeric/timestamp) skip NULL in both, so a plain count suffices.
	countExpr(col string, distinct, stringClass bool) string
	// mapNumCast renders a guard-then-cast of a map value to numeric for use inside
	// an aggregate (non-numbers excluded). The RAW key is passed; each dialect
	// applies its OWN string-literal escaping (Postgres doubles the quote;
	// ClickHouse must also escape the backslash, which it processes in literals).
	mapNumCast(col, key string) string
	// quoteIdent renders a result-column alias as a quoted identifier with the
	// dialect's OWN escaping. The identifier TEXT is the same across dialects (so
	// column names — hence results — stay identical); only the quoting differs.
	// Postgres doubles the double-quote; ClickHouse must also escape the backslash.
	quoteIdent(s string) string
}

// escapePGLiteral escapes a string for a Postgres single-quoted literal
// (standard_conforming_strings: only the quote needs doubling).
func escapePGLiteral(s string) string { return strings.ReplaceAll(s, "'", "''") }

// escapeCHLiteral escapes a string for a ClickHouse single-quoted literal, which
// processes C-style backslash escapes: the backslash MUST be escaped first, then
// the quote — otherwise a trailing backslash escapes the closing quote and breaks
// out of the literal (the injection the Postgres-only escaping missed).
func escapeCHLiteral(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "'", `\'`)
}

// ---------------------------------------------------------------------------
// Postgres dialect — the reference. Emission here MUST stay byte-identical to
// the pre-dialect compiler (compile_test.go / agg_test.go pin these strings).
// ---------------------------------------------------------------------------

type pgDialect struct{}

func (pgDialect) name() string             { return "postgres" }
func (pgDialect) placeholder(n int) string { return "$" + strconv.Itoa(n) }
func (pgDialect) boolLiteral(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

var pgComputed = map[string]string{
	"duration": "EXTRACT(EPOCH FROM (end_time - start_time))",
	"ttft":     "EXTRACT(EPOCH FROM (completion_start_time - start_time))",
}

func (pgDialect) computedCol(token string) (string, bool) { s, ok := pgComputed[token]; return s, ok }

func (pgDialect) nullSafeNeq(col, ph string, _ fieldClass) string {
	return col + " IS DISTINCT FROM " + ph
}
func (pgDialect) isNull(col string, _ fieldClass) string { return col + " IS NULL" }
func (pgDialect) inArray(col, ph string) string          { return col + " = ANY(" + ph + ")" }
func (pgDialect) notInArray(col, ph string, _ fieldClass) string {
	return "(" + col + " IS NULL OR " + col + " <> ALL(" + ph + "))"
}
func (pgDialect) mapExtractText(col, keyPh string) string { return col + " ->> " + keyPh }
func (pgDialect) mapHasKey(col, keyPh string) string      { return col + " ? " + keyPh }
func (pgDialect) mapNumGuard(col, keyPh1, keyPh2, cmp, valPh, elseVal string) string {
	typeExpr := "jsonb_typeof(" + col + " -> " + keyPh1 + ")"
	castExpr := "(" + col + " ->> " + keyPh2 + ")::numeric " + cmp + " " + valPh
	return "(CASE WHEN " + typeExpr + " = 'number' THEN " + castExpr + " ELSE " + elseVal + " END)"
}
func (pgDialect) numGuardNeqOp() string             { return "IS DISTINCT FROM" }
func (pgDialect) refField(col, field string) string { return col + " ->> '" + field + "'" }

func (pgDialect) timeBucket(interval, anchor string) string {
	return "date_bin('" + interval + "', " + anchor + ", TIMESTAMPTZ '1970-01-01')"
}
func (pgDialect) numericAggExpr(op, col string) (string, bool) {
	switch op {
	case "sum":
		return "SUM(" + col + ")::float8", true
	case "avg":
		return "AVG(" + col + ")::float8", true
	case "min":
		return "MIN(" + col + ")::float8", true
	case "max":
		return "MAX(" + col + ")::float8", true
	case "count":
		return "COUNT(" + col + ")", true
	case "p50", "p90", "p95", "p99":
		frac := "0." + op[1:]
		return "(PERCENTILE_CONT(" + frac + ") WITHIN GROUP (ORDER BY " + col + "))::float8", true
	}
	return "", false
}
func (pgDialect) mapNumCast(col, key string) string {
	k := escapePGLiteral(key)
	return "CASE WHEN jsonb_typeof(" + col + " -> '" + k + "') = 'number' THEN (" +
		col + " ->> '" + k + "')::numeric END"
}
func (pgDialect) quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func (pgDialect) countExpr(col string, distinct, _ bool) string {
	// Postgres COUNT already skips NULL (its unset sentinel) for every class.
	if distinct {
		return "COUNT(DISTINCT " + col + ")"
	}
	return "COUNT(" + col + ")"
}

// ---------------------------------------------------------------------------
// ClickHouse dialect. JSON columns hold JSON text (String), so map access uses
// JSON* functions; string columns are non-null '' (absent == ''), numeric/
// timestamp columns are Nullable. See §0 N1 / the adapter for the read model.
// ---------------------------------------------------------------------------

type chDialect struct{}

func (chDialect) name() string             { return "clickhouse" }
func (chDialect) placeholder(_ int) string { return "?" }
func (chDialect) boolLiteral(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

var chComputed = map[string]string{
	// Fractional seconds via nanosecond difference; NULL when an endpoint is NULL
	// (Nullable end/completion times), matching Postgres EXTRACT(EPOCH ...) over a
	// NULL interval.
	"duration": "(toUnixTimestamp64Nano(end_time) - toUnixTimestamp64Nano(start_time)) / 1e9",
	"ttft":     "(toUnixTimestamp64Nano(completion_start_time) - toUnixTimestamp64Nano(start_time)) / 1e9",
}

func (chDialect) computedCol(token string) (string, bool) { s, ok := chComputed[token]; return s, ok }

// stringClass reports whether the column stores an empty-string sentinel for
// "unset" (CH string columns) rather than a real NULL (Nullable numeric/timestamp).
func stringClass(c fieldClass) bool {
	switch c {
	case classString, classEnum:
		return true
	}
	return false
}

func (chDialect) nullSafeNeq(col, ph string, class fieldClass) string {
	if stringClass(class) {
		// No NULLs in a CH string column, so `!=` already matches every non-equal
		// row including the '' (unset) sentinel — NULL-safe by construction.
		return col + " != " + ph
	}
	// Nullable numeric/timestamp: NULL IS DISTINCT FROM v is true, so unset matches.
	return "(" + col + " IS NULL OR " + col + " != " + ph + ")"
}
func (chDialect) isNull(col string, class fieldClass) string {
	if stringClass(class) {
		return col + " = ''"
	}
	return col + " IS NULL"
}
func (chDialect) inArray(col, ph string) string { return col + " IN " + ph }
func (chDialect) notInArray(col, ph string, class fieldClass) string {
	if stringClass(class) {
		// '' (unset) is not in a caller list of real values, so plain NOT IN matches
		// unset rows — same observable result as Postgres's IS NULL OR <> ALL.
		return col + " NOT IN " + ph
	}
	return "(" + col + " IS NULL OR " + col + " NOT IN " + ph + ")"
}
func (chDialect) mapExtractText(col, keyPh string) string {
	return "JSONExtractString(" + col + ", " + keyPh + ")"
}
func (chDialect) mapHasKey(col, keyPh string) string {
	return "JSONHas(" + col + ", " + keyPh + ")"
}
func (chDialect) mapNumGuard(col, keyPh1, keyPh2, cmp, valPh, elseVal string) string {
	// JSONType returns Int64/UInt64/Double for JSON numbers; guard on that set so
	// the JSONExtractFloat cast is only trusted for real numbers (JSONExtractFloat
	// yields 0 for non-numbers, which the guard's ELSE excludes).
	typeExpr := "JSONType(" + col + ", " + keyPh1 + ") IN ('Int64','UInt64','Double')"
	castExpr := "JSONExtractFloat(" + col + ", " + keyPh2 + ") " + cmp + " " + valPh
	return "(CASE WHEN " + typeExpr + " THEN " + castExpr + " ELSE " + elseVal + " END)"
}
func (chDialect) numGuardNeqOp() string { return "!=" }
func (chDialect) refField(col, field string) string {
	return "JSONExtractString(" + col + ", '" + field + "')"
}

func (chDialect) timeBucket(interval, anchor string) string {
	// Map the Postgres interval literal ("1 minute") to a CH INTERVAL.
	return "toStartOfInterval(" + anchor + ", INTERVAL " + strings.ToUpper(interval) + ")"
}
func (chDialect) numericAggExpr(op, col string) (string, bool) {
	switch op {
	case "sum":
		return "toFloat64(sum(" + col + "))", true
	case "avg":
		return "toFloat64(avg(" + col + "))", true
	case "min":
		return "toFloat64(min(" + col + "))", true
	case "max":
		return "toFloat64(max(" + col + "))", true
	case "count":
		return "count(" + col + ")", true
	case "p50", "p90", "p95", "p99":
		frac := "0." + op[1:]
		return chPercentileCont(frac, col), true
	}
	return "", false
}

// chPercentileCont reproduces Postgres PERCENTILE_CONT EXACTLY: linear
// interpolation at 0-indexed rank p·(n−1) over the sorted non-null values. No
// built-in ClickHouse quantile matches this (quantileExact doesn't interpolate;
// quantileInterpolatedWeighted uses a different rank formula — both verified to
// diverge), so it is computed from the sorted groupArray. NULLs are skipped by
// groupArray, matching PERCENTILE_CONT; an empty group yields NULL.
func chPercentileCont(p, col string) string {
	g := "arraySort(groupArray(" + col + "))"
	n := "length(" + g + ")"
	rank := "(" + p + " * (" + n + " - 1))"
	lo := "toUInt64(floor(" + rank + "))"
	loVal := "arrayElement(" + g + ", " + lo + " + 1)"
	hiVal := "arrayElement(" + g + ", least(" + lo + " + 2, " + n + "))"
	return "toFloat64(if(" + n + " = 0, NULL, " + loVal + " + (" + rank + " - " + lo + ") * (" + hiVal + " - " + loVal + ")))"
}
func (chDialect) mapNumCast(col, key string) string {
	k := escapeCHLiteral(key)
	return "CASE WHEN JSONType(" + col + ", '" + k + "') IN ('Int64','UInt64','Double') THEN " +
		"JSONExtractFloat(" + col + ", '" + k + "') END"
}

// quoteIdent uses ClickHouse backtick quoting with backslash+backtick escaping
// (ClickHouse processes C-style escapes inside quoted identifiers, so doubling
// alone is unsafe). The identifier text matches Postgres's, so column names — and
// therefore results — stay identical across dialects.
func (chDialect) quoteIdent(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "`", "\\`")
	return "`" + s + "`"
}
func (chDialect) countExpr(col string, distinct, stringClass bool) string {
	if stringClass {
		// Exclude the '' unset sentinel so counts match Postgres, which stores NULL
		// for unset and skips it. uniqExact is an exact distinct count.
		if distinct {
			return "uniqExactIf(" + col + ", " + col + " != '')"
		}
		return "countIf(" + col + " != '')"
	}
	if distinct {
		return "count(DISTINCT " + col + ")"
	}
	return "count(" + col + ")"
}
