package normalize

import (
	"strings"

	"github.com/m7mdhka/llmobs/kernel/pkg/brand"
)

const maxEnvironmentLen = 40

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
