package dualstore

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// MergeTraces unifies traces across the boundary. A trace is synthesized FROM
// spans, so a trace whose spans straddle both stores would otherwise appear as two
// partial traces. For any trace id present in BOTH (a split trace) it re-synthesizes
// from the union of the trace's spans (via getSpans, which unions both stores) so
// its fields (times, span_count, is_open, incomplete_trace, status) are computed
// over the whole trace. Single-store traces (the common case) pass through.
func MergeTraces(
	ctx context.Context, scaleTraces, liteTraces []json.RawMessage, keys []OrderKey, limit int,
	getSpans func(ctx context.Context, projectID, traceID string) ([]json.RawMessage, error),
) ([]json.RawMessage, error) {
	byID := map[string]json.RawMessage{}
	inScale := map[string]bool{}
	for _, tr := range scaleTraces {
		id := traceID(tr)
		byID[id] = tr
		inScale[id] = true
	}
	var split []string
	for _, tr := range liteTraces {
		id := traceID(tr)
		if inScale[id] {
			split = append(split, id) // present in both → split, re-synthesize below
			continue
		}
		byID[id] = tr
	}

	for _, id := range split {
		projectID, _ := decode(byID[id])["project_id"].(string)
		spans, serr := getSpans(ctx, projectID, id)
		if serr != nil {
			return nil, serr
		}
		if full := SynthesizeTrace(spans); full != nil {
			byID[id] = full
		}
	}

	out := make([]json.RawMessage, 0, len(byID))
	for _, tr := range byID {
		out = append(out, tr)
	}
	less := docLess(keys)
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func traceID(traceDoc json.RawMessage) string {
	id, _ := decode(traceDoc)["id"].(string)
	return id
}

// synthesizeTrace builds a trace doc from a trace's spans (already deduped +
// ordered by GetTraceSpans), mirroring the adapters' SQL trace projection so a
// re-synthesized split trace matches a single-store one. Returns nil for no spans.
func SynthesizeTrace(spans []json.RawMessage) json.RawMessage {
	if len(spans) == 0 {
		return nil
	}
	type sp struct {
		id, parent, name, env, rel, ver, sess, user, status, kind string
		start                                                     time.Time
		end                                                       *time.Time
		attrs                                                     map[string]any
		cost                                                      float64
		hasCost                                                   bool
	}
	var parsed []sp
	ids := map[string]bool{}
	var projectID, traceIDVal string
	for _, raw := range spans {
		m := decode(raw)
		p := sp{
			id:     asString(m["id"]),
			parent: asString(m["parent_span_id"]),
			name:   asString(m["name"]),
			env:    asString(m["environment"]),
			rel:    asString(m["release"]),
			ver:    asString(m["version"]),
			sess:   asString(m["session_id"]),
			user:   asString(m["user_id"]),
		}
		if st, ok := m["status"].(map[string]any); ok {
			p.status = asString(st["code"])
		}
		if t, ok := asTime(m["start_time"]); ok {
			p.start = t
		}
		if t, ok := asTime(m["end_time"]); ok {
			p.end = &t
		}
		if a, ok := m["attributes"].(map[string]any); ok {
			p.attrs = a
		}
		p.kind = asString(m["kind"])
		if c, ok := m["total_cost"].(float64); ok {
			p.cost = c
			p.hasCost = true
		}
		parsed = append(parsed, p)
		ids[p.id] = true
		if projectID == "" {
			projectID = asString(m["project_id"])
		}
		if traceIDVal == "" {
			traceIDVal = asString(m["trace_id"])
		}
	}

	// Root: a span with an empty parent, else the earliest span (spans arrive
	// ordered by (start_time,id), so the first with an empty parent, else the first).
	root := parsed[0]
	for _, p := range parsed {
		if p.parent == "" {
			root = p
			break
		}
	}

	minStart := parsed[0].start
	var maxEnd *time.Time
	var maxStart = parsed[0].start
	anyError := false
	isOpen := false
	incomplete := false
	var traceCost float64
	hasTraceCost := false
	for _, p := range parsed {
		if p.start.Before(minStart) {
			minStart = p.start
		}
		if p.start.After(maxStart) {
			maxStart = p.start
		}
		if p.end == nil {
			isOpen = true
		} else if maxEnd == nil || p.end.After(*maxEnd) {
			maxEnd = p.end
		}
		if p.status == "error" {
			anyError = true
		}
		if p.parent != "" && !ids[p.parent] {
			incomplete = true // orphan-with-parent-ref (Collector dropped the parent)
		}
		// trace-level cost (§7.1): sum only NON-aggregate spans' cost, matching the SQL
		// projections' `kind NOT IN (agg kinds)` so a re-synthesized split trace's
		// total_cost is identical to a single-store one.
		if p.hasCost && !storage.IsAggregateKind(p.kind) {
			traceCost += p.cost
			hasTraceCost = true
		}
	}
	lastActivity := maxStart
	if maxEnd != nil {
		lastActivity = *maxEnd
	}
	status := root.status
	if anyError {
		status = "error"
	}

	doc := map[string]any{
		"id":            traceIDVal,
		"project_id":    projectID,
		"name":          root.name,
		"start_time":    minStart.UTC().Format(time.RFC3339Nano),
		"status":        map[string]any{"code": status},
		"environment":   root.env,
		"release":       root.rel,
		"version":       root.ver,
		"session_id":    root.sess,
		"user_id":       root.user,
		"tags":          []any{},
		"span_count":    int64(len(parsed)),
		"is_open":       isOpen,
		"last_activity": lastActivity.UTC().Format(time.RFC3339Nano),
	}
	if incomplete {
		doc["llmobs.dq.incomplete_trace"] = true
	}
	if maxEnd != nil {
		doc["end_time"] = maxEnd.UTC().Format(time.RFC3339Nano)
	}
	if hasTraceCost {
		// Round to the shared scale so a re-synthesized split trace's total_cost is
		// byte-identical to a single-store adapter's rounded projection.
		doc["total_cost"] = storage.RoundCost(traceCost)
	}
	if root.attrs != nil {
		doc["attributes"] = root.attrs
	}
	b, _ := json.Marshal(doc)
	return b
}

// AggSpec is the dialect-neutral aggregation shape the merge needs: which result
// columns form the group key, and the canonical op behind each aggregate column. It
// comes from the compiled query, NOT from parsing column names — so a caller-supplied
// alias can never be mistaken for a group column or a different op.
type AggSpec struct {
	GroupCols []string          // result columns forming the group key (g0,g1,…)
	Ops       map[string]string // aggregate result column -> canonical op
}

// mergeAggregation combines group rows across the lite∪scale straddle. Summary-
// mergeable ops combine exactly (count/count* → add, sum → add, min → min, max →
// max); non-mergeable ops (avg, count_distinct, percentiles) CANNOT be reconstructed
// from partial per-store results, so scale's partial value is kept and the column is
// reported as a non-mergeable straddle (the caller surfaces a warning). Returns the
// merged rows and the sorted set of aggregate columns that straddled non-mergeably.
func MergeAggregation(scaleRows, liteRows []map[string]any, spec AggSpec) ([]map[string]any, []string) {
	byKey := map[string]map[string]any{}
	order := []string{}
	straddled := map[string]bool{}
	add := func(row map[string]any) {
		k := groupKey(row, spec.GroupCols)
		existing, ok := byKey[k]
		if !ok {
			cp := map[string]any{}
			for kk, vv := range row {
				cp[kk] = vv
			}
			byKey[k] = cp
			order = append(order, k)
			return
		}
		// This group exists in both stores → a genuine straddle for its aggregates.
		for col, op := range spec.Ops {
			merged, mergeable := combine(op, existing[col], row[col])
			existing[col] = merged
			if !mergeable {
				straddled[col] = true
			}
		}
	}
	for _, r := range scaleRows {
		add(r)
	}
	for _, r := range liteRows {
		add(r)
	}
	out := make([]map[string]any, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	cols := make([]string, 0, len(straddled))
	for c := range straddled {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	return out, cols
}

func groupKey(row map[string]any, groupCols []string) string {
	var b strings.Builder
	for _, c := range groupCols {
		b.WriteString(asString(fmtVal(row[c])))
		b.WriteByte(0)
	}
	return b.String()
}

func fmtVal(v any) any {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// mergeableOps are the aggregation ops whose per-store partials combine exactly.
// count (COUNT(*) or COUNT(field)) is additive; count_distinct is NOT (overlapping
// distinct values would be double-counted).
var mergeableOps = map[string]bool{"count": true, "sum": true, "min": true, "max": true}

// combine merges two partial aggregate values for the same group by the column's OP
// (not its name). Returns the merged value and whether the op was summary-mergeable;
// a non-mergeable op keeps scale's partial value and reports mergeable=false.
func combine(op string, scaleVal, liteVal any) (any, bool) {
	if !mergeableOps[op] {
		// avg / count_distinct / p50..p99 across a straddle are NOT reconstructable from
		// partials; keep scale's value and flag it (the caller warns). count_distinct is
		// explicitly here — summing it double-counts overlapping distinct values (ADR-0026 D8).
		return scaleVal, false
	}
	fa, oka := asFloat(scaleVal)
	fb, okb := asFloat(liteVal)
	if !oka || !okb {
		return scaleVal, true
	}
	switch op {
	case "count", "sum":
		return fa + fb, true
	case "min":
		if fb < fa {
			return fb, true
		}
		return fa, true
	case "max":
		if fb > fa {
			return fb, true
		}
		return fa, true
	}
	return scaleVal, true
}
