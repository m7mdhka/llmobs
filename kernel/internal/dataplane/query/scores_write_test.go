package query

import "testing"

func baseScore() map[string]any {
	return map[string]any{
		"id": "sc1", "subject_type": "span", "subject_id": "span-1",
		"name": "helpfulness", "data_type": "numeric", "value_numeric": 0.8,
		"source": "eval", "timestamp": "2026-07-01T00:00:00Z", "environment": "production",
	}
}

func TestValidateScoreOK(t *testing.T) {
	if err := validateScore(baseScore()); err != nil {
		t.Fatalf("valid numeric score rejected: %v", err)
	}
}

func TestValidateScoreValueUnion(t *testing.T) {
	// numeric with a string value_string set -> mismatch
	s := baseScore()
	s["value_string"] = "nope"
	if err := validateScore(s); err == nil || err.Code != "score_type_mismatch" {
		t.Fatalf("numeric+value_string should mismatch, got %v", err)
	}
	// categorical requires value_string
	c := baseScore()
	c["data_type"] = "categorical"
	delete(c, "value_numeric")
	if err := validateScore(c); err == nil || err.Code != "score_type_mismatch" {
		t.Fatalf("categorical without value_string should mismatch, got %v", err)
	}
	c["value_string"] = "good"
	if err := validateScore(c); err != nil {
		t.Fatalf("valid categorical rejected: %v", err)
	}
	// boolean must be 0/1
	b := baseScore()
	b["data_type"] = "boolean"
	b["value_numeric"] = 0.5
	if err := validateScore(b); err == nil || err.Code != "score_type_mismatch" {
		t.Fatalf("boolean 0.5 should mismatch, got %v", err)
	}
}

func TestValidateScoreSubjectType(t *testing.T) {
	// plugin-namespaced subject type is accepted (LM-8, no registry gate)
	s := baseScore()
	s["subject_type"] = "rag/document"
	if err := validateScore(s); err != nil {
		t.Fatalf("namespaced subject_type should be accepted: %v", err)
	}
	// an unqualified non-kernel type is rejected
	s["subject_type"] = "widget"
	if err := validateScore(s); err == nil || err.Status != 400 {
		t.Fatalf("unqualified non-kernel subject_type should be 400, got %v", err)
	}
}

func TestValidateScoreNoCoercion(t *testing.T) {
	s := baseScore()
	s["value_numeric"] = "0.8" // string, not number
	if err := validateScore(s); err == nil || err.Code != "score_type_mismatch" {
		t.Fatalf("string value for numeric must not be coerced, got %v", err)
	}
}
