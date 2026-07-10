package main

import (
	"encoding/json"
	"net/url"
	"sort"
	"time"
)

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func toInt(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		i, _ := t.Int64()
		return i
	}
	return 0
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	}
	return 0
}

// pick returns a new map with only the requested keys that are present.
func pick(m map[string]any, keys ...string) map[string]any {
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

func durationMs(start, end any) int64 {
	s, sok := start.(string)
	e, eok := end.(string)
	if !sok || !eok {
		return -1
	}
	st, err1 := time.Parse(time.RFC3339Nano, s)
	et, err2 := time.Parse(time.RFC3339Nano, e)
	if err1 != nil || err2 != nil || et.Before(st) {
		return -1
	}
	return et.Sub(st).Milliseconds()
}

func jsonResult(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func urlEscape(s string) string { return url.PathEscape(s) }

// --- JSON-Schema helpers for tool input schemas ---

func obj(props map[string]any, required []string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func enumProp(vals []string, desc string) map[string]any {
	return map[string]any{"type": "string", "enum": vals, "description": desc}
}

// sortStable sorts a slice in place by a less function.
func sortStable[T any](rows []T, less func(a, b T) bool) {
	sort.SliceStable(rows, func(i, j int) bool { return less(rows[i], rows[j]) })
}
