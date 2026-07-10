package query

import "encoding/json"

// Payload fields projected out for metadata-scoped callers (DSL §10). These are
// the prompt/completion/message-carrying fields; everything else (promoted
// dimensions, status, usage/cost, top-level llmobs.dq.* signals) is metadata and
// survives. `attributes` is stripped wholesale because flattened prompt arrays
// (gen_ai.prompt.N.*) live there — the conservative contract. Kernel-owned
// llmobs.* metadata that lives *inside* attributes (llmobs.raw.*, llmobs.dq.*) is
// stripped with it; the interesting-edge note is in the PR report.
var payloadFields = []string{"input", "output", "events", "attributes", "model_parameters"}

// scoreMetadataStrip: a metadata-scoped caller does not receive a score's freeform
// comment (04-score.md; DSL §10 "score metadata/comment").
var scorePayloadFields = []string{"comment"}

// stripPayloadFields removes the given keys from a JSON doc, returning the
// projected doc. On any decode failure the original is returned unchanged (a
// metadata caller must never get *more* than intended, and a valid doc always
// decodes; a malformed one is not our payload to leak).
func stripPayloadFields(raw json.RawMessage, fields []string) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	changed := false
	for _, f := range fields {
		if _, ok := m[f]; ok {
			delete(m, f)
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

// projectRows strips payload fields from every row when the caller lacks payload
// scope. A no-op for payload-scoped callers.
func projectRows(rows []json.RawMessage, fields []string, canReadPayloads bool) []json.RawMessage {
	if canReadPayloads {
		return rows
	}
	out := make([]json.RawMessage, len(rows))
	for i, r := range rows {
		out[i] = stripPayloadFields(r, fields)
	}
	return out
}
