// Package redact scrubs PII/secret patterns from payload fields BEFORE they are
// persisted — the invariant that makes exports and compliance safe by
// construction. It is pattern-based and conservative;
// the advanced NER/processor-injection path for custom models remains tracked.
//
// Redaction is observable: silent scrubbing is how trust dies. Every scrub is
// counted and surfaced as llmobs.dq.redacted so a consumer knows it happened.
package redact

import "regexp"

// Rule is one detector: a compiled pattern and the token that replaces a match.
type Rule struct {
	Name  string
	re    *regexp.Regexp
	token string
	luhn  bool // if set, only redact digit-runs that pass the Luhn check
}

// Preset detectors (conservative; each replacement token names the class).
var presets = map[string]Rule{
	"email":       {Name: "email", re: regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`), token: "[REDACTED:email]"},
	"phone":       {Name: "phone", re: regexp.MustCompile(`(?:\+\d{1,3}[\s.\-]?)?\(?\d{3}\)?[\s.\-]?\d{3}[\s.\-]?\d{4}\b`), token: "[REDACTED:phone]"},
	"credit_card": {Name: "credit_card", re: regexp.MustCompile(`\b(?:\d[ \-]?){13,19}\b`), token: "[REDACTED:credit_card]", luhn: true},
	"iban":        {Name: "iban", re: regexp.MustCompile(`\b[A-Z]{2}\d{2}[A-Z0-9]{11,30}\b`), token: "[REDACTED:iban]"},
	"secret":      {Name: "secret", re: regexp.MustCompile(`\b(?:sk-[A-Za-z0-9]{16,}|AKIA[0-9A-Z]{16}|gh[posru]_[A-Za-z0-9]{20,}|xox[baprs]-[A-Za-z0-9\-]{10,})\b`), token: "[REDACTED:secret]"},
}

// DefaultPresets is the conservative default set. Order matters: structured
// patterns (secret, iban, credit_card) run BEFORE the greedy phone pattern so a
// phone-shaped digit run inside an IBAN/card is not matched first.
var DefaultPresets = []string{"email", "secret", "iban", "credit_card", "phone"}

// CustomRule is a per-deployment regex rule from config.
type CustomRule struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
	Token   string `json:"token"`
}

// Redactor applies an ordered rule set to string leaves of a payload.
type Redactor struct {
	rules []Rule
}

// New builds a redactor from preset names + custom rules. Unknown presets and
// uncompilable custom patterns are skipped (a bad rule must not disable the rest).
func New(presetNames []string, custom []CustomRule) *Redactor {
	r := &Redactor{}
	for _, name := range presetNames {
		if p, ok := presets[name]; ok {
			r.rules = append(r.rules, p)
		}
	}
	for _, c := range custom {
		re, err := regexp.Compile(c.Pattern)
		if err != nil {
			continue
		}
		token := c.Token
		if token == "" {
			token = "[REDACTED:" + c.Name + "]"
		}
		r.rules = append(r.rules, Rule{Name: c.Name, re: re, token: token})
	}
	return r
}

// Enabled reports whether any rule is active.
func (r *Redactor) Enabled() bool { return len(r.rules) > 0 }

// RedactString scrubs a single string, returning the result and per-rule counts.
func (r *Redactor) RedactString(s string) (string, map[string]int) {
	counts := map[string]int{}
	for _, rule := range r.rules {
		rule := rule
		s = rule.re.ReplaceAllStringFunc(s, func(match string) string {
			if rule.luhn && !luhnValid(match) {
				return match // not a real card number — leave it
			}
			counts[rule.Name]++
			return rule.token
		})
	}
	return s, counts
}

// RedactValue walks any JSON value and scrubs string leaves in place, returning
// the scrubbed value and aggregated counts. Non-string leaves are untouched;
// object KEYS are never touched (keys are structure, not payload).
func (r *Redactor) RedactValue(v any) (any, map[string]int) {
	total := map[string]int{}
	var walk func(any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case string:
			out, c := r.RedactString(t)
			mergeCounts(total, c)
			return out
		case map[string]any:
			for k, val := range t {
				t[k] = walk(val)
			}
			return t
		case []any:
			for i, val := range t {
				t[i] = walk(val)
			}
			return t
		default:
			return x
		}
	}
	return walk(v), total
}

func mergeCounts(dst, src map[string]int) {
	for k, v := range src {
		dst[k] += v
	}
}

// luhnValid reports whether the digits in s pass the Luhn checksum.
func luhnValid(s string) bool {
	var digits []int
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits = append(digits, int(r-'0'))
		}
	}
	if len(digits) < 13 {
		return false
	}
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := digits[i]
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

// TotalCount sums a per-rule count map.
func TotalCount(counts map[string]int) int {
	t := 0
	for _, v := range counts {
		t += v
	}
	return t
}

// CountsToSignal renders the dq signal value: total + per-rule counts.
func CountsToSignal(counts map[string]int) map[string]any {
	out := map[string]any{"total": TotalCount(counts)}
	for k, v := range counts {
		out[k] = v
	}
	return out
}
