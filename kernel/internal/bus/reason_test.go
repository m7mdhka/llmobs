package bus

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSanitizeReasonAlwaysValidUTF8 is the regression for a byte-boundary truncation bug:
// capping a UTF-8 reason at a byte offset can split a multibyte rune, yielding invalid
// UTF-8 that a UTF-8 Postgres DLQ column rejects — which would fail the DeadLetter write,
// 500 the /fail call, and leave the poison event un-dead-letterable. The reason MUST be
// capped by rune so the stored value is always valid UTF-8.
func TestSanitizeReasonAlwaysValidUTF8(t *testing.T) {
	cases := []string{
		strings.Repeat("é", 500),        // 2-byte runes, well over the byte cap
		strings.Repeat("你", 400),        // 3-byte runes (CJK)
		strings.Repeat("🔥", 300),        // 4-byte runes (emoji)
		strings.Repeat("a", 1000),       // ASCII over the cap
		"café\x00\x07 malformed\x1b[0m", // control chars mixed in
		"",                              // empty → placeholder
	}
	for _, in := range cases {
		got := sanitizeReason(in)
		if !utf8.ValidString(got) {
			t.Fatalf("sanitizeReason produced invalid UTF-8 for input %q", in)
		}
		if !strings.HasPrefix(got, "subscriber:") {
			t.Fatalf("reason must be namespaced with the subscriber: prefix, got %q", got)
		}
		body := strings.TrimPrefix(got, "subscriber:")
		if utf8.RuneCountInString(body) > maxFailReasonLen {
			t.Fatalf("reason body exceeds the %d-rune cap: %d runes", maxFailReasonLen, utf8.RuneCountInString(body))
		}
		// Control characters must not survive.
		for _, r := range body {
			if r < 0x20 || r == 0x7F {
				t.Fatalf("control character %#U survived sanitization in %q", r, got)
			}
		}
	}

	// An empty / all-control reason becomes the stable placeholder.
	if got := sanitizeReason("\x00\x01\x02"); got != "subscriber:unspecified" {
		t.Fatalf("all-control reason should be the placeholder, got %q", got)
	}
}
