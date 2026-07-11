package pluginsettings

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ValidationError names the offending field so the client can surface it.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("settings field %q: %s", e.Field, e.Reason)
}

// Validate checks an incoming settings write against the model. `incoming` is the
// user-submitted values (raw JSON per field). `alreadySet` names secret fields that
// already have a stored value — a required secret is satisfied if it is being
// provided now OR was previously set (so re-saving a form does not force retyping a
// secret). It is a pure function: no I/O, table-testable.
//
//   - unknown fields (not in the model) are rejected (strictness — the schema is the
//     contract);
//   - required fields must be present (or, for secrets, already set);
//   - present values must match the field type, enum, and bounds.
//
// A provided secret value of "" is treated as "not provided" (the preserve signal),
// handled by the store; Validate does not fault an empty secret.
func Validate(m *Model, incoming map[string]json.RawMessage, alreadySet map[string]bool) error {
	if m.Custom {
		return validateCustom(m, incoming)
	}
	for name := range incoming {
		if _, ok := m.Field(name); !ok {
			return &ValidationError{Field: name, Reason: "unknown field (not in settings schema)"}
		}
	}
	for _, f := range m.Fields {
		raw, present := incoming[f.Name]
		if f.Secret && present && isEmptyString(raw) {
			present = false // empty secret == preserve existing; treat as absent
		}
		if !present {
			if f.Required && !(f.Secret && alreadySet[f.Name]) {
				return &ValidationError{Field: f.Name, Reason: "required"}
			}
			continue
		}
		if err := validateValue(f, raw); err != nil {
			return err
		}
	}
	return nil
}

// validateCustom checks a custom-mode write (N2): the plugin owns validation of its
// opaque non-secret values, so the kernel only enforces that (a) a declared SECRET field
// is a string (or empty = preserve — it is stored encrypted), and (b) every value is
// itself valid JSON. The total-size ceiling is enforced by the store against the merged
// document (MaxValueBytes). Unknown keys are ALLOWED (that is the whole point of custom
// mode) — but a key colliding with a declared secret name must obey the secret rule.
func validateCustom(m *Model, incoming map[string]json.RawMessage) error {
	for name, raw := range incoming {
		if !json.Valid(raw) {
			return &ValidationError{Field: name, Reason: "must be valid JSON"}
		}
		if f, ok := m.Field(name); ok && f.Secret {
			if isEmptyString(raw) {
				continue // preserve signal
			}
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return &ValidationError{Field: name, Reason: "secret must be a string"}
			}
		}
	}
	return nil
}

func validateValue(f Field, raw json.RawMessage) error {
	switch f.Type {
	case TypeString:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return &ValidationError{Field: f.Name, Reason: "must be a string"}
		}
		if f.MinLength != nil && len(s) < *f.MinLength {
			return &ValidationError{Field: f.Name, Reason: fmt.Sprintf("must be at least %d characters", *f.MinLength)}
		}
		if len(f.Enum) > 0 && !contains(f.Enum, s) {
			return &ValidationError{Field: f.Name, Reason: "must be one of the allowed values"}
		}
	case TypeBoolean:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return &ValidationError{Field: f.Name, Reason: "must be a boolean"}
		}
	case TypeNumber, TypeInteger:
		var n float64
		if err := json.Unmarshal(raw, &n); err != nil {
			return &ValidationError{Field: f.Name, Reason: "must be a number"}
		}
		if f.Type == TypeInteger && n != float64(int64(n)) {
			return &ValidationError{Field: f.Name, Reason: "must be an integer"}
		}
		if f.Minimum != nil && n < *f.Minimum {
			return &ValidationError{Field: f.Name, Reason: fmt.Sprintf("must be >= %v", *f.Minimum)}
		}
		if f.Maximum != nil && n > *f.Maximum {
			return &ValidationError{Field: f.Name, Reason: fmt.Sprintf("must be <= %v", *f.Maximum)}
		}
	default:
		return &ValidationError{Field: f.Name, Reason: "unsupported field type"}
	}
	return nil
}

func isEmptyString(raw json.RawMessage) bool {
	var s string
	return json.Unmarshal(raw, &s) == nil && s == ""
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// AsValidationError reports whether err is a ValidationError (a client 400), so the
// handler can distinguish bad input from an internal fault.
func AsValidationError(err error) (*ValidationError, bool) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve, true
	}
	return nil, false
}
