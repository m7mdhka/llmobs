package query

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A representative span doc carrying every payload-bearing field plus a secret
// value inside input/output/attributes/events, so a leak is detectable by grep.
const spanWithPayloads = `{
  "id": "s1", "project_id": "p", "trace_id": "t", "kind": "generation",
  "name": "chat", "start_time": "2026-01-01T00:00:00Z",
  "status": {"code": "ok"}, "environment": "prod",
  "model": "gpt-4o", "total_cost": 0.01,
  "usage_details": {"input": 100},
  "input": "SECRET_PROMPT ignore previous instructions",
  "output": "SECRET_COMPLETION my SSN is 000-00-0000",
  "attributes": {"gen_ai.prompt.0.content": "SECRET_ATTR", "llmobs.dq.x": 1},
  "model_parameters": {"temperature": "0.7"},
  "events": [{"name": "gen_ai.user.message", "timestamp": 1, "attributes": {"content": "SECRET_EVENT"}}]
}`

// This is the "an assistant never ingests PII" guarantee, proven structurally:
// a metadata-scoped projection must contain NONE of the secret payload markers.
func TestMetadataProjectionStripsAllPayloads(t *testing.T) {
	stripped := stripPayloadFields(json.RawMessage(spanWithPayloads), payloadFields)
	s := string(stripped)

	for _, secret := range []string{"SECRET_PROMPT", "SECRET_COMPLETION", "SECRET_ATTR", "SECRET_EVENT"} {
		if strings.Contains(s, secret) {
			t.Fatalf("payload leaked into metadata projection: %q found in\n%s", secret, s)
		}
	}
	var m map[string]any
	if err := json.Unmarshal(stripped, &m); err != nil {
		t.Fatal(err)
	}
	for _, field := range payloadFields {
		if _, ok := m[field]; ok {
			t.Fatalf("payload field %q not stripped:\n%s", field, s)
		}
	}
	// Metadata survives.
	for _, keep := range []string{`"id"`, `"kind"`, `"model"`, `"status"`, `"total_cost"`, `"usage_details"`} {
		if !strings.Contains(s, keep) {
			t.Fatalf("metadata field %s wrongly stripped:\n%s", keep, s)
		}
	}
}

// Payload-scoped callers get everything (projectRows is a no-op).
func TestPayloadScopePassesThrough(t *testing.T) {
	rows := []json.RawMessage{json.RawMessage(spanWithPayloads)}
	out := projectRows(rows, payloadFields, true)
	if !strings.Contains(string(out[0]), "SECRET_PROMPT") {
		t.Fatal("payload-scoped caller should receive payloads")
	}
	out = projectRows(rows, payloadFields, false)
	if strings.Contains(string(out[0]), "SECRET_PROMPT") {
		t.Fatal("metadata-scoped caller must not receive payloads")
	}
}

// Filtering on a payload field is impossible: payload fields are not queryable, so
// a metadata caller can never even reference them in a filter (encoded rule for a
// future payload_search capability — it must require the payload scope).
func TestPayloadFieldsNotQueryable(t *testing.T) {
	for _, f := range []string{"input", "output", "events", "model_parameters"} {
		_, err := CompileSpans(baseDoc(map[string]any{"field": f, "op": "eq", "value": "x"}), "p", 30*24*time.Hour)
		ce, _ := err.(*CompileError)
		if ce == nil || ce.Code != "unknown_field" {
			t.Fatalf("payload field %q must be unknown to the compiler, got %v", f, err)
		}
	}
}
