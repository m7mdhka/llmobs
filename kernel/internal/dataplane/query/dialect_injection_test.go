package query

import (
	"strings"
	"testing"
	"time"
)

// TestCHLiteralEscapesBackslash locks the fix for the ClickHouse-specific
// injection: ClickHouse processes C-style backslash escapes inside string
// literals, so a trailing backslash in a user-supplied map key would escape the
// closing quote and break out. Postgres-style quote-doubling does not defend it.
func TestCHLiteralEscapesBackslash(t *testing.T) {
	tests := []struct{ in, want string }{
		{`plain`, `plain`},
		{`a'b`, `a\'b`},
		{`a\`, `a\\`},
		{`\'`, `\\\'`}, // backslash escaped first, then quote
		{`x\' OR 1=1 --`, `x\\\' OR 1=1 --`},
	}
	for _, tt := range tests {
		if got := escapeCHLiteral(tt.in); got != tt.want {
			t.Errorf("escapeCHLiteral(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestCHMapNumCastNoBreakout proves a malicious aggregation key cannot break out
// of the JSON-function string literal on the ClickHouse path: after escaping, the
// single quotes in the emitted SQL are balanced (every `'` that is not part of a
// `\'` escape pairs up).
func TestCHMapNumCastNoBreakout(t *testing.T) {
	malicious := `k') OR (SELECT 1 FROM spans WHERE '1'='1`
	sql := chDialect{}.mapNumCast("usage_details", malicious)
	// The unescaped breakout `k')` (a bare quote terminating the literal early) must
	// NOT appear — the escaper turns it into the safe `k\')`.
	if strings.Contains(sql, "k')") {
		t.Fatalf("key broke out of the literal: %s", sql)
	}
	if !strings.Contains(sql, `k\')`) {
		t.Fatalf("expected the hostile quote to be backslash-escaped: %s", sql)
	}
}

// TestCHQuoteIdentEscapes proves an alias / derived column name cannot break out
// of a ClickHouse backtick identifier.
func TestCHQuoteIdentEscapes(t *testing.T) {
	got := chDialect{}.quoteIdent("a`b\\c")
	if got != "`a\\`b\\\\c`" {
		t.Errorf("quoteIdent = %q", got)
	}
}

// TestAggregationKeyCompilesSafely runs the full aggregation compile with a
// hostile map key and confirms it produces a Compiled result (no panic) with the
// key confined to escaped literal positions, on the ClickHouse dialect.
func TestAggregationKeyCompilesSafely(t *testing.T) {
	doc := map[string]any{
		"target":    "spans",
		"timeRange": map[string]any{"from": "2026-01-01T00:00:00Z", "to": "2026-01-02T00:00:00Z"},
		"aggregations": []any{map[string]any{
			"op": "sum", "field": "usage_details", "key": `evil\' UNION SELECT`,
		}},
	}
	ca, err := CompileAggregationForDialect(doc, "p", 30*24*time.Hour, "spans", ClickHouseDialect)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if strings.Contains(ca.Select, `evil\' UNION`) && !strings.Contains(ca.Select, `evil\\\' UNION`) {
		t.Fatalf("hostile key not fully escaped in SELECT: %s", ca.Select)
	}
}
