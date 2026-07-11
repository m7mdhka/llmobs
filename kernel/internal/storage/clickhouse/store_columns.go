package clickhouse

import (
	"encoding/json"
	"time"
)

// Column extractors for the wide settled row. The authoritative value is always
// the `doc` (canonical JSON); these promote the queryable/indexed fields out of
// the merged doc into typed columns, mirroring the lite adapter's extraction but
// with ClickHouse-native types (plain String defaults "" instead of NULL; a
// non-null value for the required DateTime64 sort-key columns).

// str returns a plain String column value ("" when absent).
func str(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// statusStr extracts status.code as a plain String ("" when absent).
func statusStr(m map[string]any) string {
	if st, ok := m["status"].(map[string]any); ok {
		if code, ok := st["code"].(string); ok {
			return code
		}
	}
	return ""
}

// timeReq returns a non-null DateTime64 value, falling back to `fallback` (the
// event timestamp) when the field is absent/unparseable — the column is a
// required sort/partition key and must never be NULL. A malformed span with no
// start_time is still storable, keyed by its arrival time.
func timeReq(m map[string]any, key string, fallback time.Time) time.Time {
	if t := parseTime(m, key); t != nil {
		return *t
	}
	return fallback.UTC()
}

// timeOpt returns a *time.Time for a Nullable(DateTime64) column (nil when absent).
func timeOpt(m map[string]any, key string) *time.Time {
	return parseTime(m, key)
}

func parseTime(m map[string]any, key string) *time.Time {
	s, ok := m[key].(string)
	if !ok || s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, s); err != nil {
			return nil
		}
	}
	tu := t.UTC()
	return &tu
}

// floatOpt returns a *float64 for a Nullable(Float64) column (nil when absent).
func floatOpt(m map[string]any, key string) *float64 {
	switch v := m[key].(type) {
	case float64:
		return &v
	case int:
		f := float64(v)
		return &f
	}
	return nil
}

// jsonMap marshals a map-valued field to a JSON String, defaulting to "{}".
func jsonMap(m map[string]any, key string) string {
	if v, ok := m[key]; ok && v != nil {
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	return "{}"
}

// jsonRef marshals an optional ref-valued field to a JSON String, "" when absent.
func jsonRef(m map[string]any, key string) string {
	if v, ok := m[key]; ok && v != nil {
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	return ""
}

// boolU8 maps a bool field to ClickHouse UInt8 (0/1).
func boolU8(m map[string]any, key string) uint8 {
	if b, ok := m[key].(bool); ok && b {
		return 1
	}
	return 0
}
