package normalize

import (
	"strings"

	"github.com/m7mdhka/llmobs/kernel/pkg/brand"
)

const maxEnvironmentLen = 40

// SanitizeAttributeKeys strips ASCII control characters (U+0000–U+001F, U+007F)
// from attribute keys (02-span.md §6.3). It runs in the shared normalize stage so
// the rule is identical across transports. For each offending key the sanitized
// key carries the value, the original is preserved under
// `llmobs.raw.attr_key.<sanitized>`, and `llmobs.dq.sanitized_attribute_keys` is
// incremented. This makes an adapter's control-char field-path separator
// collision-free by construction. Mutates and returns attrs.
func SanitizeAttributeKeys(attrs map[string]any) map[string]any {
	if attrs == nil {
		return attrs
	}
	var offending []string
	for k := range attrs {
		if hasControlChar(k) {
			offending = append(offending, k)
		}
	}
	for _, k := range offending {
		clean := stripControlChars(k)
		v := attrs[k]
		delete(attrs, k)
		attrs[clean] = v
		attrs["llmobs.raw.attr_key."+clean] = k
		n, _ := attrs["llmobs.dq.sanitized_attribute_keys"].(int)
		attrs["llmobs.dq.sanitized_attribute_keys"] = n + 1
	}
	return attrs
}

func hasControlChar(s string) bool {
	for _, r := range s {
		if r <= 0x1F || r == 0x7F {
			return true
		}
	}
	return false
}

func stripControlChars(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r <= 0x1F || r == 0x7F {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// SanitizeEnvironment is the single dimension-sanitization routine (LM-11,
// 08-data-quality.md §2), used by every transport including compat plugins. It
// returns the sanitized value and whether it was coerced/changed (so the caller
// can preserve the raw value and raise the data-quality signal). An empty result
// coerces to "default".
func SanitizeEnvironment(offered string) (sanitized string, changed bool) {
	s := strings.ToLower(strings.TrimSpace(offered))
	// strip a leading reserved brand prefix (e.g. "llmobs" / "llmobs-")
	prefix := strings.ToLower(brand.Name)
	s = strings.TrimPrefix(s, prefix+"-")
	if s == strings.ToLower(brand.Name) {
		s = ""
	}
	// charset: replace anything outside [a-z0-9._-] with '-'
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s = b.String()
	if len(s) > maxEnvironmentLen {
		s = s[:maxEnvironmentLen]
	}
	if s == "" {
		s = "default"
	}
	return s, s != offered
}
