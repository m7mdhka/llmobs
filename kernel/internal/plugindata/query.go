package plugindata

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Op is a store filter operator (R4: comparison only — no aggregations, no joins).
type Op string

const (
	OpEq  Op = "eq"
	OpNe  Op = "ne"
	OpLt  Op = "lt"
	OpLte Op = "lte"
	OpGt  Op = "gt"
	OpGte Op = "gte"
)

var opSQL = map[Op]string{OpEq: "=", OpNe: "<>", OpLt: "<", OpLte: "<=", OpGt: ">", OpGte: ">="}

// Filter is one predicate on a declared indexed field.
type Filter struct {
	Field string `json:"field"`
	Op    Op     `json:"op"`
	Value any    `json:"value"`
}

// Order sorts by a declared indexed field (id is always the final tiebreak).
type Order struct {
	Field string `json:"field"`
	Desc  bool   `json:"desc"`
}

// Query is the store's small read surface (R4).
type Query struct {
	Filters []Filter `json:"filters,omitempty"`
	Order   *Order   `json:"order,omitempty"`
	Limit   int      `json:"limit,omitempty"`
	Cursor  string   `json:"cursor,omitempty"`
}

const (
	DefaultLimit = 100
	MaxLimit     = 500
)

// cursorPos is the keyset position encoded in an opaque cursor.
type cursorPos struct {
	O  any    `json:"o,omitempty"` // order-field value (nil when ordering by id)
	ID string `json:"id"`
}

// EncodeCursor renders a keyset cursor for the last row of a page.
func EncodeCursor(orderVal any, id string) string {
	b, _ := json.Marshal(cursorPos{O: orderVal, ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (cursorPos, error) {
	var c cursorPos
	if s == "" {
		return c, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, fmt.Errorf("plugindata: bad cursor")
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("plugindata: bad cursor")
	}
	return c, nil
}

// CompileSelect builds the tenant-scoped SELECT for a collection query. It ALWAYS
// scopes by project_id ($1) — the cross-tenant isolation invariant — and rejects
// filters/orders on fields that are not declared indexed (R4). Returns the SQL,
// its args, and the effective limit (callers fetch limit+1 to derive the next
// cursor). schema/table MUST already be validated + quoted-safe identifiers.
func CompileSelect(schema, table string, indexed map[string]FieldSpec, projectID string, q Query) (string, []any, int, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}

	var b strings.Builder
	fmt.Fprintf(&b, `SELECT id, doc FROM %s.%s WHERE project_id = $1`, quoteIdent(schema), quoteIdent(table))
	args := []any{projectID}

	for _, f := range q.Filters {
		if _, ok := indexed[f.Field]; !ok {
			return "", nil, 0, fmt.Errorf("plugindata: field %q is not a declared indexed field", f.Field)
		}
		sqlOp, ok := opSQL[f.Op]
		if !ok {
			return "", nil, 0, fmt.Errorf("plugindata: unsupported operator %q", f.Op)
		}
		args = append(args, f.Value)
		fmt.Fprintf(&b, ` AND %s %s $%d`, quoteIdent(f.Field), sqlOp, len(args))
	}

	// Keyset pagination + ordering. id is always the final, ascending tiebreak.
	cur, err := decodeCursor(q.Cursor)
	if err != nil {
		return "", nil, 0, err
	}
	if q.Order != nil {
		if _, ok := indexed[q.Order.Field]; !ok {
			return "", nil, 0, fmt.Errorf("plugindata: order field %q is not a declared indexed field", q.Order.Field)
		}
		col := quoteIdent(q.Order.Field)
		cmp := ">"
		if q.Order.Desc {
			cmp = "<"
		}
		if q.Cursor != "" {
			args = append(args, cur.O)
			oPos := len(args)
			args = append(args, cur.ID)
			idPos := len(args)
			// (F cmp $o) OR (F = $o AND id > $id)
			fmt.Fprintf(&b, ` AND (%s %s $%d OR (%s = $%d AND id > $%d))`, col, cmp, oPos, col, oPos, idPos)
		}
		dir := "ASC"
		if q.Order.Desc {
			dir = "DESC"
		}
		fmt.Fprintf(&b, ` ORDER BY %s %s, id ASC`, col, dir)
	} else {
		if q.Cursor != "" {
			args = append(args, cur.ID)
			fmt.Fprintf(&b, ` AND id > $%d`, len(args))
		}
		b.WriteString(` ORDER BY id ASC`)
	}
	b.WriteString(` LIMIT ` + strconv.Itoa(limit+1))
	return b.String(), args, limit, nil
}

// quoteIdent double-quotes a (pre-validated) SQL identifier.
func quoteIdent(s string) string { return `"` + s + `"` }
