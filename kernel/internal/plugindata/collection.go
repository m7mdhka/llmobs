// Package plugindata defines the `store` primitive's contract (ADR-0023 / the
// eighth primitive): manifest-declared typed collections a plugin owns, and the
// deliberately-small query surface over them (R4: filter/order/paginate on
// declared indexed fields; no aggregations, no joins). It holds the types,
// identifier sanitization, and the pure query compiler — the boundary a
// PluginStore adapter (Postgres for lite; a dedicated-DB backend for scale, R1)
// implements. Depends on nothing kernel-internal.
package plugindata

import (
	"fmt"
	"regexp"
)

// FieldType is a collection field's declared type.
type FieldType string

const (
	FieldString FieldType = "string"
	FieldNumber FieldType = "number"
	FieldBool   FieldType = "boolean"
	FieldJSON   FieldType = "json"
)

// FieldSpec declares one field of a collection. Only string/number/boolean fields
// may be indexed (and thus filterable/orderable); json fields are opaque payload.
type FieldSpec struct {
	Name    string    `json:"name"`
	Type    FieldType `json:"type"`
	Indexed bool      `json:"indexed"`
}

// CollectionSpec is a plugin-declared collection (one table in the plugin's
// schema).
type CollectionSpec struct {
	Name   string      `json:"name"`
	Fields []FieldSpec `json:"fields"`
}

// identifiers are validated so they can be safely quoted into DDL/DML. Manifest
// validation already constrains these, but the store re-checks (defence in depth).
var (
	collectionRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	fieldRe      = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	pluginIDRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9-]*$`)
)

// Validate checks the collection spec's identifiers and field types.
func (c CollectionSpec) Validate() error {
	if !collectionRe.MatchString(c.Name) {
		return fmt.Errorf("plugindata: invalid collection name %q", c.Name)
	}
	seen := map[string]bool{}
	for _, f := range c.Fields {
		if !fieldRe.MatchString(f.Name) {
			return fmt.Errorf("plugindata: invalid field name %q", f.Name)
		}
		if seen[f.Name] {
			return fmt.Errorf("plugindata: duplicate field %q", f.Name)
		}
		seen[f.Name] = true
		switch f.Type {
		case FieldString, FieldNumber, FieldBool, FieldJSON:
		default:
			return fmt.Errorf("plugindata: invalid field type %q", f.Type)
		}
		if f.Indexed && f.Type == FieldJSON {
			return fmt.Errorf("plugindata: json field %q cannot be indexed", f.Name)
		}
	}
	return nil
}

// Indexed returns the indexed field specs (the filterable/orderable set).
func (c CollectionSpec) Indexed() map[string]FieldSpec {
	out := map[string]FieldSpec{}
	for _, f := range c.Fields {
		if f.Indexed {
			out[f.Name] = f
		}
	}
	return out
}

// SchemaName renders the Postgres schema for a plugin id (R1: plugin-namespaced
// schemas in the shared DB). Must be validated before use.
func SchemaName(pluginID string) (string, error) {
	if !pluginIDRe.MatchString(pluginID) {
		return "", fmt.Errorf("plugindata: invalid plugin id %q", pluginID)
	}
	// owner/name -> plugin_owner__name (slash->__, hyphen->_), all lower-safe.
	s := "plugin_"
	for _, r := range pluginID {
		switch {
		case r == '/':
			s += "__"
		case r == '-':
			s += "_"
		default:
			s += string(r)
		}
	}
	return s, nil
}
