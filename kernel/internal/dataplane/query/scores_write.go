package query

import (
	"regexp"
	"strings"

	"github.com/m7mdhka/llmobs/kernel/internal/storage"
)

// subjectTypeRe accepts a bare kernel subject type or a plugin-namespaced one
// (ns/name). Registration ceremony is deferred (audit ruling); round-tripping is
// not — the format is validated from day one (LM-8).
var subjectTypeRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(/[a-z][a-z0-9_]*)?$`)

var kernelSubjectTypes = map[string]bool{"span": true, "trace": true, "session": true}

var validScoreSources = map[string]bool{
	"annotation": true, "eval": true, "heuristic": true, "plugin": true,
}

// validateScore enforces LM-3 (value union, no coercion) and LM-8 (subject
// format) on one score object. project_id is set by the caller from identity.
// Returns a coded error (schema_invalid 400 for structure, score_type_mismatch
// 422 for value/type union violations) — never coerces.
func validateScore(s map[string]any) *CompileError {
	req := func(k string) (string, *CompileError) {
		v, ok := s[k].(string)
		if !ok || v == "" {
			return "", errf("schema_invalid", 400, "score.%s is required", k)
		}
		return v, nil
	}
	if _, err := req("id"); err != nil {
		return err
	}
	if _, err := req("subject_id"); err != nil {
		return err
	}
	if _, err := req("name"); err != nil {
		return err
	}
	subjectType, err := req("subject_type")
	if err != nil {
		return err
	}
	if !subjectTypeRe.MatchString(subjectType) {
		return errf("schema_invalid", 400, "score.subject_type must be a kernel type or ns/name")
	}
	if strings.Contains(subjectType, "/") == false && !kernelSubjectTypes[subjectType] {
		return errf("schema_invalid", 400, "unqualified subject_type must be one of span|trace|session")
	}
	if _, ok := s["timestamp"].(string); !ok {
		return errf("schema_invalid", 400, "score.timestamp is required (RFC3339)")
	}
	if src, ok := s["source"].(string); ok && src != "" && !validScoreSources[src] {
		return errf("schema_invalid", 400, "score.source must be annotation|eval|heuristic|plugin")
	}

	dataType, _ := s["data_type"].(string)
	_, hasNum := s["value_numeric"]
	_, hasStr := s["value_string"]
	numVal, numOK := s["value_numeric"].(float64)
	strVal, strOK := s["value_string"].(string)
	switch dataType {
	case "numeric":
		if !numOK {
			return errf("score_type_mismatch", 422, "numeric score requires a numeric value_numeric")
		}
		if hasStr && s["value_string"] != nil {
			return errf("score_type_mismatch", 422, "numeric score must not set value_string")
		}
		_ = numVal
	case "boolean":
		if !numOK || (numVal != 0 && numVal != 1) {
			return errf("score_type_mismatch", 422, "boolean score value_numeric must be 0 or 1")
		}
		if hasStr && s["value_string"] != nil {
			return errf("score_type_mismatch", 422, "boolean score must not set value_string")
		}
	case "categorical":
		if !strOK || strVal == "" {
			return errf("score_type_mismatch", 422, "categorical score requires a value_string label")
		}
		_ = hasNum // value_numeric MAY carry the mapped number
	default:
		return errf("schema_invalid", 400, "score.data_type must be numeric|categorical|boolean")
	}
	return nil
}

// scoreEvent builds the merge event for a validated score. The score's timestamp
// is the identity/merge anchor (04 §5); event id is the score id.
func scoreEvent(s map[string]any) storage.Event {
	ts, _ := parseTime(s["timestamp"])
	id, _ := s["id"].(string)
	return storage.Event{
		Op:      storage.OpUpsert,
		EventTS: ts.UTC(),
		EventID: id,
		Payload: s,
	}
}
