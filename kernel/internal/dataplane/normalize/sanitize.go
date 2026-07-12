package normalize

import (
	"strings"

	"github.com/m7mdhka/llmobs/kernel/pkg/brand"
)

const maxEnvironmentLen = 40

// SanitizeAttributeKeys strips ASCII control characters (U+0000–U+001F, U+007F)
// from attribute keys. It runs in the shared normalize stage so
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

// SanitizeNullBytes strips NUL (U+0000) from every string VALUE in the canonical span,
// recursively (attribute values, nested maps/slices in the bag, and promoted string fields
// like input/output/name). PostgreSQL cannot store a NUL in a text or JSONB column (error
// 22P05), so a span carrying a NUL in ANY value fails the lite INSERT and is dropped — while
// the identical span lands on ClickHouse (scale), a SILENT lite-vs-scale divergence and a lost
// span. Stripping in the ONE shared normalize stage keeps the span AND makes both
// profiles byte-identical. NUL is the ONLY character Postgres rejects; other control chars
// (tab, newline) are storable and legitimate in opaque payloads, so they are left intact — we
// don't corrupt data beyond the one character that cannot be stored at all. The count of
// affected strings is stamped as a dq signal. Mutates and returns out.
func SanitizeNullBytes(out map[string]any) map[string]any {
	if out == nil {
		return out
	}
	if _, n := stripNulls(out); n > 0 {
		out["llmobs.dq.sanitized_null_bytes"] = n
	}
	return out
}

// stripNulls removes NUL from strings recursively, mutating maps/slices in place. Returns the
// (possibly replaced) value and the count of strings from which a NUL was removed.
func stripNulls(v any) (any, int) {
	switch t := v.(type) {
	case string:
		if strings.IndexByte(t, 0) >= 0 {
			return strings.ReplaceAll(t, "\x00", ""), 1
		}
		return t, 0
	case map[string]any:
		n := 0
		for k, val := range t {
			if cleaned, c := stripNulls(val); c > 0 {
				t[k] = cleaned
				n += c
			}
		}
		return t, n
	case []any:
		n := 0
		for i, val := range t {
			if cleaned, c := stripNulls(val); c > 0 {
				t[i] = cleaned
				n += c
			}
		}
		return t, n
	default:
		return v, 0
	}
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

// SanitizeEnvironment is the single dimension-sanitization routine,
// used by every transport including compat plugins. It
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
