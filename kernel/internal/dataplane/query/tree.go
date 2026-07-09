package query

import (
	"encoding/json"
	"net/http"
	"sort"
)

// GetTraceTree: GET /v1alpha1/traces/{trace_id}/tree — the trace and its spans in
// tree order (parent before children), the tree-first read the sort key exists
// for. Spans are folded docs; the trace is synthesized from the root span(s).
func (s *Server) GetTraceTree(w http.ResponseWriter, r *http.Request, traceID string) {
	ident, aerr := s.auth(r, "query")
	if aerr != nil {
		writeErr(w, aerr)
		return
	}
	rows, err := s.store.GetTraceSpans(r.Context(), ident.ProjectID, traceID)
	if err != nil {
		s.log.Error("trace tree fetch", "err", err.Error())
		writeErr(w, errf("internal", 500, "fetch failed"))
		return
	}
	if len(rows) == 0 {
		writeErr(w, errf("not_found", 404, "no such trace"))
		return
	}
	spans := decodeSpans(rows)
	ordered := treeOrder(spans)
	trace := synthesizeTrace(ident.ProjectID, traceID, ordered)

	writeJSON(w, http.StatusOK, map[string]any{
		"trace": trace,
		"spans": ordered,
	})
}

// GetTrace: GET /v1alpha1/traces/{id} — the single synthesized trace entity.
func (s *Server) GetTrace(w http.ResponseWriter, r *http.Request, id string) {
	ident, aerr := s.auth(r, "query")
	if aerr != nil {
		writeErr(w, aerr)
		return
	}
	rows, err := s.store.GetTraceSpans(r.Context(), ident.ProjectID, id)
	if err != nil {
		writeErr(w, errf("internal", 500, "fetch failed"))
		return
	}
	if len(rows) == 0 {
		writeErr(w, errf("not_found", 404, "no such trace"))
		return
	}
	trace := synthesizeTrace(ident.ProjectID, id, treeOrder(decodeSpans(rows)))
	writeJSON(w, http.StatusOK, trace)
}

func decodeSpans(rows []json.RawMessage) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, raw := range rows {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err == nil {
			out = append(out, m)
		}
	}
	return out
}

// treeOrder returns spans in preorder DFS (parent before children). A span whose
// parent_span_id is empty or points outside this trace is a root. Roots and each
// child list are ordered by (start_time, id) — the input is already sorted so,
// keyed by parent, that order is preserved deterministically.
func treeOrder(spans []map[string]any) []map[string]any {
	byID := make(map[string]map[string]any, len(spans))
	for _, sp := range spans {
		if id, _ := sp["id"].(string); id != "" {
			byID[id] = sp
		}
	}
	children := map[string][]map[string]any{}
	var roots []map[string]any
	for _, sp := range spans {
		parent, _ := sp["parent_span_id"].(string)
		if parent == "" {
			roots = append(roots, sp)
			continue
		}
		if _, ok := byID[parent]; ok {
			children[parent] = append(children[parent], sp)
		} else {
			// orphan (parent not in this trace) — treat as a root so it is not lost.
			roots = append(roots, sp)
		}
	}

	out := make([]map[string]any, 0, len(spans))
	var visit func(sp map[string]any)
	seen := map[string]bool{}
	visit = func(sp map[string]any) {
		id, _ := sp["id"].(string)
		if id != "" {
			if seen[id] {
				return // cycle guard
			}
			seen[id] = true
		}
		out = append(out, sp)
		for _, ch := range children[id] {
			visit(ch)
		}
	}
	for _, root := range roots {
		visit(root)
	}
	// Any span not reached (e.g. inside a cycle) is appended in sorted order so
	// the response never silently drops rows.
	for _, sp := range spans {
		id, _ := sp["id"].(string)
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, sp)
		}
	}
	return out
}

// synthesizeTrace derives a trace entity (trace.schema.json) from its spans: the
// id is the trace_id, times span the earliest start to the latest end, and the
// dimensional fields come from the first root span. Lite has no first-class
// trace row yet; this is the read-time projection.
func synthesizeTrace(projectID, traceID string, spans []map[string]any) map[string]any {
	trace := map[string]any{
		"id":          traceID,
		"project_id":  projectID,
		"environment": "",
		"attributes":  map[string]any{},
	}
	if len(spans) == 0 {
		return trace
	}
	root := spans[0] // preorder: first is a root
	for _, k := range []string{"name", "environment", "release", "version", "session_id", "user_id"} {
		if v, ok := root[k]; ok {
			trace[k] = v
		}
	}
	starts := make([]string, 0, len(spans))
	var latestEnd string
	for _, sp := range spans {
		if st, ok := sp["start_time"].(string); ok && st != "" {
			starts = append(starts, st)
		}
		if et, ok := sp["end_time"].(string); ok && et > latestEnd {
			latestEnd = et
		}
	}
	if len(starts) > 0 {
		sort.Strings(starts) // RFC3339 UTC is lexically sortable
		trace["start_time"] = starts[0]
	}
	if latestEnd != "" {
		trace["end_time"] = latestEnd
	}
	return trace
}
