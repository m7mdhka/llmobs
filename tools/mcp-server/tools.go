package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Result discipline for LLM callers: hard caps and summarized rows. An assistant
// needs enough to reason, not full documents — big payloads waste context and the
// caller can drill in with get_trace_tree/get_span.
const (
	defaultLimit = 20
	maxLimit     = 100
)

// toolDef is an MCP tool advertised to the model. Descriptions are prompts —
// treat them as contract surface: they must tell the model exactly what the tool
// returns and how to page.
type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func toolDefs() []toolDef {
	return []toolDef{
		{
			Name: "query_traces",
			Description: "List recent traces (metadata only — no prompt/response text). Returns compact rows: " +
				"id, name, start_time, duration_ms, span_count, environment, status, is_open, incomplete. " +
				"Use named filters, not raw query language. Page with the returned cursor.",
			InputSchema: obj(map[string]any{
				"environment": strProp("Filter to one environment, e.g. 'production'."),
				"status":      enumProp([]string{"ok", "error", "unset"}, "Filter by trace status."),
				"since_hours": intProp("Look back this many hours (default 24, max 720)."),
				"limit":       intProp(fmt.Sprintf("Max rows (default %d, max %d).", defaultLimit, maxLimit)),
				"cursor":      strProp("Opaque cursor from a previous call, to fetch the next page."),
			}, nil),
		},
		{
			Name:        "get_trace_tree",
			Description: "Fetch one trace's span tree (metadata only). Returns the trace plus compact spans in parent-before-child order: id, parent_span_id, name, kind, duration_ms, status.",
			InputSchema: obj(map[string]any{"trace_id": strProp("The trace id.")}, []string{"trace_id"}),
		},
		{
			Name:        "list_recent_errors",
			Description: "List recent error spans (status=error, metadata only). Compact rows: trace_id, span_id, name, kind, start_time, model. Start here to investigate incidents.",
			InputSchema: obj(map[string]any{
				"since_hours": intProp("Look back this many hours (default 24, max 720)."),
				"limit":       intProp(fmt.Sprintf("Max rows (default %d, max %d).", defaultLimit, maxLimit)),
			}, nil),
		},
		{
			Name:        "top_costs",
			Description: "Top total_cost aggregated over a dimension (model|provider|environment|session_id|user_id). Returns rows of {group, total_cost, span_count} sorted desc. Use to find what's expensive.",
			InputSchema: obj(map[string]any{
				"by":          enumProp([]string{"model", "provider", "environment", "session_id", "user_id"}, "Dimension to group by (default model)."),
				"since_hours": intProp("Look back this many hours (default 24, max 720)."),
				"limit":       intProp(fmt.Sprintf("Max groups (default %d, max %d).", defaultLimit, maxLimit)),
			}, nil),
		},
		{
			Name:        "get_span",
			Description: "Fetch one span by id (metadata only): id, trace_id, parent_span_id, name, kind, times, duration_ms, status, model, provider, usage, cost.",
			InputSchema: obj(map[string]any{"span_id": strProp("The span id.")}, []string{"span_id"}),
		},
	}
}

// callTool dispatches a tool call, returning the text content for the model.
func (s *server) callTool(ctx context.Context, name string, args map[string]any) (string, error) {
	switch name {
	case "query_traces":
		return s.queryTraces(ctx, args)
	case "get_trace_tree":
		return s.getTraceTree(ctx, args)
	case "list_recent_errors":
		return s.listRecentErrors(ctx, args)
	case "top_costs":
		return s.topCosts(ctx, args)
	case "get_span":
		return s.getSpan(ctx, args)
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

func (s *server) queryTraces(ctx context.Context, args map[string]any) (string, error) {
	from, to := windowFrom(args)
	var filters []any
	if env := str(args, "environment"); env != "" {
		filters = append(filters, map[string]any{"field": "environment", "op": "eq", "value": env})
	}
	if st := str(args, "status"); st != "" {
		filters = append(filters, map[string]any{"field": "status.code", "op": "eq", "value": st})
	}
	doc := map[string]any{
		"target":    "traces",
		"timeRange": map[string]any{"from": from, "to": to},
		"orderBy":   []any{map[string]any{"field": "start_time", "dir": "desc"}},
		"limit":     clampLimit(args),
	}
	if len(filters) > 0 {
		doc["filters"] = filters
	}
	if cur := str(args, "cursor"); cur != "" {
		doc["cursor"] = cur
	}
	qr, err := s.client.query(ctx, doc)
	if err != nil {
		return "", err
	}
	rows := make([]map[string]any, 0, len(qr.Data))
	for _, d := range qr.Data {
		var t map[string]any
		if json.Unmarshal(d, &t) != nil {
			continue
		}
		rows = append(rows, summarizeTrace(t))
	}
	return jsonResult(map[string]any{"traces": rows, "cursor": qr.Cursor, "count": len(rows)})
}

func (s *server) getTraceTree(ctx context.Context, args map[string]any) (string, error) {
	id := str(args, "trace_id")
	if id == "" {
		return "", fmt.Errorf("trace_id is required")
	}
	t, err := s.client.traceTree(ctx, id)
	if err != nil {
		return "", err
	}
	spans, _ := t["spans"].([]any)
	out := make([]map[string]any, 0, len(spans))
	for _, sp := range spans {
		if m, ok := sp.(map[string]any); ok {
			out = append(out, summarizeSpan(m))
		}
	}
	trace, _ := t["trace"].(map[string]any)
	return jsonResult(map[string]any{"trace": summarizeTrace(trace), "spans": out, "span_count": len(out)})
}

func (s *server) listRecentErrors(ctx context.Context, args map[string]any) (string, error) {
	from, to := windowFrom(args)
	doc := map[string]any{
		"target":    "spans",
		"timeRange": map[string]any{"from": from, "to": to},
		"filters":   []any{map[string]any{"field": "status.code", "op": "eq", "value": "error"}},
		"orderBy":   []any{map[string]any{"field": "start_time", "dir": "desc"}},
		"limit":     clampLimit(args),
	}
	qr, err := s.client.query(ctx, doc)
	if err != nil {
		return "", err
	}
	rows := make([]map[string]any, 0, len(qr.Data))
	for _, d := range qr.Data {
		var sp map[string]any
		if json.Unmarshal(d, &sp) != nil {
			continue
		}
		rows = append(rows, pick(sp, "trace_id", "id", "name", "kind", "start_time", "model"))
	}
	return jsonResult(map[string]any{"errors": rows, "cursor": qr.Cursor, "count": len(rows)})
}

func (s *server) topCosts(ctx context.Context, args map[string]any) (string, error) {
	from, to := windowFrom(args)
	by := str(args, "by")
	if by == "" {
		by = "model"
	}
	doc := map[string]any{
		"target":       "spans",
		"timeRange":    map[string]any{"from": from, "to": to},
		"groupBy":      []any{by},
		"aggregations": []any{map[string]any{"op": "sum", "field": "total_cost", "alias": "total_cost"}, map[string]any{"op": "count", "alias": "span_count"}},
	}
	qr, err := s.client.query(ctx, doc)
	if err != nil {
		return "", err
	}
	type row struct {
		Group     any     `json:"group"`
		TotalCost float64 `json:"total_cost"`
		SpanCount int64   `json:"span_count"`
	}
	rows := make([]row, 0, len(qr.Data))
	for _, d := range qr.Data {
		var g map[string]any
		if json.Unmarshal(d, &g) != nil {
			continue
		}
		rows = append(rows, row{Group: g["g0"], TotalCost: toFloat(g["total_cost"]), SpanCount: toInt(g["span_count"])})
	}
	sortStable(rows, func(a, b row) bool { return a.TotalCost > b.TotalCost })
	limit := clampLimit(args)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return jsonResult(map[string]any{"by": by, "top": rows})
}

func (s *server) getSpan(ctx context.Context, args map[string]any) (string, error) {
	id := str(args, "span_id")
	if id == "" {
		return "", fmt.Errorf("span_id is required")
	}
	sp, err := s.client.span(ctx, id)
	if err != nil {
		return "", err
	}
	return jsonResult(summarizeSpan(sp))
}

// --- summarizers: compact, metadata-only projections ---

func summarizeTrace(t map[string]any) map[string]any {
	out := pick(t, "id", "name", "start_time", "end_time", "environment", "session_id", "user_id", "span_count", "is_open")
	if st, ok := t["status"].(map[string]any); ok {
		out["status"] = st["code"]
	}
	if d := durationMs(t["start_time"], t["end_time"]); d >= 0 {
		out["duration_ms"] = d
	}
	if v, ok := t["llmobs.dq.incomplete_trace"]; ok {
		out["incomplete"] = v
	}
	return out
}

func summarizeSpan(sp map[string]any) map[string]any {
	out := pick(sp, "id", "trace_id", "parent_span_id", "name", "kind", "start_time", "end_time", "model", "provider")
	if st, ok := sp["status"].(map[string]any); ok {
		out["status"] = st["code"]
	}
	if u, ok := sp["usage_details"].(map[string]any); ok && len(u) > 0 {
		out["usage"] = u
	}
	if tc, ok := sp["total_cost"]; ok {
		out["total_cost"] = tc
	}
	if d := durationMs(sp["start_time"], sp["end_time"]); d >= 0 {
		out["duration_ms"] = d
	}
	return out
}

func windowFrom(args map[string]any) (string, string) {
	hours := toInt(args["since_hours"])
	if hours <= 0 {
		hours = 24
	}
	if hours > 720 {
		hours = 720
	}
	now := time.Now().UTC()
	return now.Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339)
}

func clampLimit(args map[string]any) int {
	l := int(toInt(args["limit"]))
	if l <= 0 {
		return defaultLimit
	}
	if l > maxLimit {
		return maxLimit
	}
	return l
}
