// Package pluginsettings implements the plugin settings store: settings is the `kv`
// primitive made frontend-reachable and schema-aware. It parses
// the small JSON Schema subset a settings form uses, validates a settings write
// against it, and persists values — envelope-encrypting `writeOnly` (secret) fields
// and NEVER returning them. The same subset drives packages/schema-form, so client
// and kernel validate identically.
package pluginsettings

import (
	"encoding/json"
	"fmt"
	"sort"
)

// FieldType is the JSON-Schema type of a settings field (the supported subset).
type FieldType string

const (
	TypeString  FieldType = "string"
	TypeNumber  FieldType = "number"
	TypeInteger FieldType = "integer"
	TypeBoolean FieldType = "boolean"
)

// Field is one settings field distilled from the schema.
type Field struct {
	Name        string
	Type        FieldType
	Title       string
	Description string
	Enum        []string // string enum choices, when present
	Secret      bool     // writeOnly: true — encrypted on write, never returned
	Required    bool
	Default     json.RawMessage
	MinLength   *int
	Minimum     *float64
	Maximum     *float64
}

// Model is the parsed settings schema: a flat set of fields (settings are one level;
// nested objects/arrays are future work). In CUSTOM mode the plugin mounts its own
// settings view and non-secret values are stored opaquely; Fields then holds ONLY the
// declared secret (writeOnly) fields, so those are still encrypted on write and never
// returned, while any other key is accepted and persisted as-is (bounded by MaxValueBytes).
type Model struct {
	Fields []Field
	Custom bool
}

// MaxValueBytes bounds the serialized size of a CUSTOM plugin's stored settings document
// (opaque non-secret values are un-schema'd, so they need an explicit ceiling — a plugin
// must not turn its project-shared settings row into unbounded storage). 64 KiB is far
// beyond any real settings form and well under a problematic row size.
const MaxValueBytes = 64 << 10

// Field returns the named field, or false.
func (m *Model) Field(name string) (Field, bool) {
	for _, f := range m.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// SecretFields returns the names of the write-only (secret) fields.
func (m *Model) SecretFields() []string {
	var out []string
	for _, f := range m.Fields {
		if f.Secret {
			out = append(out, f.Name)
		}
	}
	return out
}

// rawSchema is the subset of JSON Schema we read.
type rawSchema struct {
	Type       string              `json:"type"`
	Required   []string            `json:"required"`
	Properties map[string]propSpec `json:"properties"`
}

type propSpec struct {
	Type        string          `json:"type"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Enum        []string        `json:"enum"`
	WriteOnly   bool            `json:"writeOnly"`
	Default     json.RawMessage `json:"default"`
	MinLength   *int            `json:"minLength"`
	Minimum     *float64        `json:"minimum"`
	Maximum     *float64        `json:"maximum"`
}

// ParseSchema distills the JSON Schema subset into a Model. It rejects anything
// outside the supported subset (root must be an object; property types must be one
// of the four) so an unsupported schema fails loudly rather than silently dropping
// fields. Field order follows the schema's property order is not defined by JSON, so
// fields are sorted by name for a stable, deterministic Model (rendering order in the
// form is the client's concern).
func ParseSchema(raw []byte, custom bool) (*Model, error) {
	// Custom mode: the plugin renders its own view and stores opaque non-secret JSON. A
	// schema is OPTIONAL and read ONLY for its writeOnly (secret) string fields (so they
	// stay encrypted and never returned); an empty/absent schema simply means "no secrets,
	// everything opaque". We do
	// NOT reject non-subset property types here — the custom view owns validation.
	if custom {
		return parseCustom(raw)
	}
	var rs rawSchema
	if err := json.Unmarshal(raw, &rs); err != nil {
		return nil, fmt.Errorf("parse settings schema: %w", err)
	}
	if rs.Type != "" && rs.Type != "object" {
		return nil, fmt.Errorf("settings schema root must be type object, got %q", rs.Type)
	}
	required := map[string]bool{}
	for _, r := range rs.Required {
		required[r] = true
	}
	m := &Model{}
	for name, p := range rs.Properties {
		ft := FieldType(p.Type)
		switch ft {
		case TypeString, TypeNumber, TypeInteger, TypeBoolean:
		default:
			return nil, fmt.Errorf("settings field %q has unsupported type %q", name, p.Type)
		}
		if len(p.Enum) > 0 && ft != TypeString {
			return nil, fmt.Errorf("settings field %q: enum is only supported for string", name)
		}
		if p.WriteOnly && ft != TypeString {
			return nil, fmt.Errorf("settings field %q: writeOnly (secret) is only supported for string", name)
		}
		m.Fields = append(m.Fields, Field{
			Name: name, Type: ft, Title: p.Title, Description: p.Description,
			Enum: p.Enum, Secret: p.WriteOnly, Required: required[name],
			Default: p.Default, MinLength: p.MinLength, Minimum: p.Minimum, Maximum: p.Maximum,
		})
	}
	sort.Slice(m.Fields, func(i, j int) bool { return m.Fields[i].Name < m.Fields[j].Name })
	return m, nil
}

// parseCustom builds a custom-mode model: only the schema's writeOnly string fields
// become (secret) Fields; everything else is opaque. A nil/empty schema yields a model
// with no secrets. A writeOnly field that is not a string is still rejected (a secret is
// an encrypted string, same as schema mode).
func parseCustom(raw []byte) (*Model, error) {
	m := &Model{Custom: true}
	if len(raw) == 0 {
		return m, nil
	}
	var rs rawSchema
	if err := json.Unmarshal(raw, &rs); err != nil {
		return nil, fmt.Errorf("parse settings schema: %w", err)
	}
	for name, p := range rs.Properties {
		if !p.WriteOnly {
			continue // non-secret fields are opaque in custom mode
		}
		if FieldType(p.Type) != TypeString {
			return nil, fmt.Errorf("settings field %q: writeOnly (secret) is only supported for string", name)
		}
		m.Fields = append(m.Fields, Field{Name: name, Type: TypeString, Title: p.Title, Description: p.Description, Secret: true})
	}
	sort.Slice(m.Fields, func(i, j int) bool { return m.Fields[i].Name < m.Fields[j].Name })
	return m, nil
}
