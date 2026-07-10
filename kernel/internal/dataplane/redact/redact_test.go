package redact

import (
	"strings"
	"testing"
)

func TestPresetDetectors(t *testing.T) {
	r := New(DefaultPresets, nil)
	cases := []struct {
		in     string
		token  string
		redact bool
	}{
		{"contact me at jane.doe@example.com please", "[REDACTED:email]", true},
		{"call +1 (415) 555-0199 now", "[REDACTED:phone]", true},
		{"card 4111 1111 1111 1111 on file", "[REDACTED:credit_card]", true}, // valid Luhn
		{"not a card 4111 1111 1111 1112", "[REDACTED:credit_card]", false},  // fails Luhn
		{"iban DE89370400440532013000 ok", "[REDACTED:iban]", true},
		{"key sk-abcdefghij0123456789 leaked", "[REDACTED:secret]", true},
		{"just a normal sentence", "", false},
	}
	for _, c := range cases {
		out, counts := r.RedactString(c.in)
		if c.redact {
			if !strings.Contains(out, c.token) {
				t.Fatalf("%q: expected %s, got %q", c.in, c.token, out)
			}
			if TotalCount(counts) == 0 {
				t.Fatalf("%q: expected a count", c.in)
			}
		} else {
			if strings.Contains(out, "[REDACTED") {
				t.Fatalf("%q: unexpected redaction: %q", c.in, out)
			}
		}
	}
}

func TestLuhnGate(t *testing.T) {
	r := New([]string{"credit_card"}, nil)
	// A 16-digit non-card sequence must NOT be redacted.
	out, _ := r.RedactString("order number 1234567890123456 here")
	if strings.Contains(out, "[REDACTED") {
		t.Fatalf("non-Luhn digit run should not redact: %q", out)
	}
}

func TestRedactValueRecursive(t *testing.T) {
	r := New([]string{"email"}, nil)
	v := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "reach me at a@b.com"},
		},
		"count": 3,
	}
	out, counts := r.RedactValue(v)
	m := out.(map[string]any)
	msg := m["messages"].([]any)[0].(map[string]any)
	if strings.Contains(msg["content"].(string), "a@b.com") {
		t.Fatalf("nested email not redacted: %v", msg)
	}
	if m["count"] != 3 {
		t.Fatalf("non-string leaf changed: %v", m["count"])
	}
	if counts["email"] != 1 {
		t.Fatalf("expected 1 email count, got %v", counts)
	}
	// Keys are never redacted: a key that looks like an email stays.
	v2 := map[string]any{"a@b.com": "x"}
	out2, _ := r.RedactValue(v2)
	if _, ok := out2.(map[string]any)["a@b.com"]; !ok {
		t.Fatal("object key must never be redacted")
	}
}

func TestCustomRule(t *testing.T) {
	r := New(nil, []CustomRule{{Name: "ticket", Pattern: `JIRA-\d+`, Token: "[REDACTED:ticket]"}})
	out, counts := r.RedactString("see JIRA-1234 for details")
	if !strings.Contains(out, "[REDACTED:ticket]") || counts["ticket"] != 1 {
		t.Fatalf("custom rule failed: %q %v", out, counts)
	}
}

func TestDisabledWhenNoRules(t *testing.T) {
	if New(nil, nil).Enabled() {
		t.Fatal("no rules => disabled")
	}
	// A bad custom pattern is skipped, not fatal.
	r := New(nil, []CustomRule{{Name: "bad", Pattern: "("}})
	if r.Enabled() {
		t.Fatal("uncompilable rule should be skipped")
	}
}
