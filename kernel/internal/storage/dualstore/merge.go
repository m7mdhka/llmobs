package dualstore

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// OrderKey is one logical ORDER BY term the merge sorts by (dialect-neutral). The
// query layer derives these from the compiled query so the merge never has to parse
// dialect-specific ORDER SQL (a computed field like `duration` resolves to an engine
// expression, not a doc field, and would silently sort as NULL if parsed as a name).
type OrderKey struct {
	Field string
	Desc  bool
}

// mergeOrdered unifies two per-store result pages into one: dedup by
// (project_id,id) preferring scale (scaleRows are passed first), then re-sort the
// bounded union by the SAME logical order the stores used. Re-sorting ≤2×limit rows
// is cheap and avoids a fragile hand-rolled k-way merge; the result is a single
// coherent ordered page.
func MergeOrdered(scaleRows, liteRows []json.RawMessage, keys []OrderKey) []json.RawMessage {
	seen := make(map[string]bool, len(scaleRows)+len(liteRows))
	out := make([]json.RawMessage, 0, len(scaleRows)+len(liteRows))
	for _, r := range append(append([]json.RawMessage{}, scaleRows...), liteRows...) {
		k := docKey(r)
		if seen[k] {
			continue // duplicate (project_id,id) — scale already kept it
		}
		seen[k] = true
		out = append(out, r)
	}
	less := docLess(keys)
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

// dedupByID keeps the first occurrence of each (project_id,id) — callers pass
// scale rows first so scale wins.
func dedupByID(rows []json.RawMessage) []json.RawMessage {
	seen := make(map[string]bool, len(rows))
	out := make([]json.RawMessage, 0, len(rows))
	for _, r := range rows {
		k := docKey(r)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	return out
}

// docKey is the (project_id,id) identity a row dedups on.
func docKey(doc json.RawMessage) string {
	m := decode(doc)
	pid, _ := m["project_id"].(string)
	id, _ := m["id"].(string)
	return pid + "\x00" + id
}

// docLess builds a comparator matching the compiled ORDER BY from LOGICAL keys, so
// the merged union sorts identically to each backend's page. Computed fields
// (duration, ttft) are recomputed from the doc's timestamps — they are engine
// expressions, not stored doc fields, so they must be derived here.
func docLess(keys []OrderKey) func(a, b json.RawMessage) bool {
	if len(keys) == 0 {
		keys = []OrderKey{{Field: "start_time", Desc: true}, {Field: "id"}}
	}
	return func(a, b json.RawMessage) bool {
		ma, mb := decode(a), decode(b)
		for _, k := range keys {
			va, vb := orderValue(ma, k.Field), orderValue(mb, k.Field)
			// NULLS LAST for BOTH directions, matching the explicit `NULLS LAST` the
			// query compiles for both engines. A missing/null key always sorts after a
			// present one, so scale's null-valued rows are never ranked ahead of lite's
			// present ones (which would drop them off a trimmed page).
			an, bn := isNullOrder(va), isNullOrder(vb)
			if an || bn {
				if an && bn {
					continue
				}
				return !an // the non-null value sorts first (is "less")
			}
			c := compareField(va, vb)
			if c == 0 {
				continue
			}
			if k.Desc {
				return c > 0
			}
			return c < 0
		}
		return false
	}
}

// isNullOrder reports whether an order value is absent (a SQL NULL for this key).
func isNullOrder(v any) bool { return v == nil }

// orderValue resolves a logical order field to a comparable value from the doc.
// Computed fields mirror the adapters' SQL: duration = end_time - start_time (secs),
// ttft = completion_start_time - start_time (secs); nil when an endpoint is missing
// (sorts last, matching the engines' NULL-interval behavior).
func orderValue(m map[string]any, field string) any {
	switch field {
	case "duration":
		return secondsBetween(m["start_time"], m["end_time"])
	case "ttft":
		return secondsBetween(m["start_time"], m["completion_start_time"])
	default:
		return m[field]
	}
}

// secondsBetween returns (end-start) in fractional seconds, or nil if either endpoint
// is absent/unparseable.
func secondsBetween(start, end any) any {
	st, ok1 := asTime(start)
	en, ok2 := asTime(end)
	if !ok1 || !ok2 {
		return nil
	}
	return en.Sub(st).Seconds()
}

// compareField compares two doc values, trying time then number then string —
// matching how the SQL engines order these columns.
func compareField(a, b any) int {
	// nil sorts last (mirrors NULLS behavior consistently across both stores).
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return 1
	}
	if b == nil {
		return -1
	}
	if ta, oka := asTime(a); oka {
		if tb, okb := asTime(b); okb {
			switch {
			case ta.Before(tb):
				return -1
			case ta.After(tb):
				return 1
			default:
				return 0
			}
		}
	}
	if fa, oka := asFloat(a); oka {
		if fb, okb := asFloat(b); okb {
			switch {
			case fa < fb:
				return -1
			case fa > fb:
				return 1
			default:
				return 0
			}
		}
	}
	sa, sb := asString(a), asString(b)
	return strings.Compare(sa, sb)
}

// anchorRef is a (time, id) pair used to order trace spans.
type anchorRef struct {
	ts time.Time
	id string
}

func docAnchorID(doc json.RawMessage) anchorRef {
	m := decode(doc)
	id, _ := m["id"].(string)
	return anchorRef{ts: anchorTime(m), id: id}
}

// anchorTime reads the entity's ordering anchor (span/trace start_time, score
// timestamp) as a time; zero if absent/unparseable.
func anchorTime(m map[string]any) time.Time {
	for _, k := range []string{"start_time", "timestamp"} {
		if t, ok := asTime(m[k]); ok {
			return t
		}
	}
	return time.Time{}
}

func decode(doc json.RawMessage) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(doc, &m)
	return m
}

func asTime(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	}
	return 0, false
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
