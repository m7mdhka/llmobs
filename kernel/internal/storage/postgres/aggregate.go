package postgres

import (
	"context"
	"fmt"
)

// QueryAggregation runs a compiled aggregation (QD-4) for one target and returns
// group rows as maps (column name -> value). The compiler supplies the SELECT
// (group + agg expressions), WHERE, and GROUP BY; the adapter supplies the FROM
// source per target — the base table for spans/scores, the synthesized projection
// for traces.
func (s *Store) QueryAggregation(ctx context.Context, target, sel, where, groupBy string, args []any) ([]map[string]any, error) {
	var sql string
	switch target {
	case "spans":
		sql = "SELECT " + sel + " FROM spans WHERE is_deleted = false"
		if where != "" {
			sql += " AND (" + where + ")"
		}
	case "scores":
		sql = "SELECT " + sel + " FROM scores WHERE is_deleted = false"
		if where != "" {
			sql += " AND (" + where + ")"
		}
	case "traces":
		sql = "WITH trace_proj AS (" + traceProjection + ") SELECT " + sel + " FROM trace_proj"
		if where != "" {
			sql += " WHERE " + where
		}
	default:
		return nil, fmt.Errorf("unknown aggregation target %q", target)
	}
	if groupBy != "" {
		sql += " GROUP BY " + groupBy
	}
	// A bounded safety limit on group cardinality.
	sql += " LIMIT 10000"

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = string(f.Name)
	}

	var out []map[string]any
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		m := make(map[string]any, len(names))
		for i, n := range names {
			m[n] = normalizeAggValue(vals[i])
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// normalizeAggValue coerces pgx numeric types to JSON-friendly values.
func normalizeAggValue(v any) any {
	switch t := v.(type) {
	case [16]byte:
		// pgtype.Numeric or uuid come through as bytes in rare cases; stringify.
		return fmt.Sprintf("%x", t)
	default:
		return t
	}
}
