// SQL generation : bind parameters and identifier quoting.
package writer

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/samber/oops"
)

// SQLWriter is a Writer for one SQL statement. Call the generic list helpers as writer.List(w.Writer, ...).
// w.Write returns the embedded *Writer, so it cannot chain into Bind/Id.
type SQLWriter struct {
	*Writer
	slots []sqlBindSlot
}

// sqlBindSlot is one $N placeholder : a literal value (Bind, name == "") or
// a named param reserved via BindParam, sharing one $N sequence.
type sqlBindSlot struct {
	name  string
	value any
	json  bool // a named param cast to json or jsonb : sent as JSON text
	bytes bool // a named param cast through bytea : its value is the bytes
}

// NewSQL returns an SQLWriter for one SQL statement.
func NewSQL() *SQLWriter {
	return &SQLWriter{Writer: New()}
}

// Bind writes the next $N placeholder for value ; equal values are not deduplicated. The $N sequence is per
// statement, so each statement needs its own SQLWriter.
func (w *SQLWriter) Bind(value any) *SQLWriter {
	w.slots = append(w.slots, sqlBindSlot{value: value})
	w.Write(fmt.Sprintf("$%d", len(w.slots)))
	return w
}

// BindParam reserves the next $N placeholder for a well-known query's named parameter, whose value only exists per
// request. Shares Bind's $N sequence ; get the args with ResolveArgs, not Args. asJSON marks a placeholder cast to
// json or jsonb, whose value ResolveArgs writes as JSON text.
func (w *SQLWriter) BindParam(name string, asJSON bool) *SQLWriter {
	w.slots = append(w.slots, sqlBindSlot{name: name, json: asJSON})
	w.Write(fmt.Sprintf("$%d", len(w.slots)))
	return w
}

// BindBytesParam is BindParam for a param whose value is bytes, the SQL casting it from bytea.
func (w *SQLWriter) BindBytesParam(name string) *SQLWriter {
	w.slots = append(w.slots, sqlBindSlot{name: name, bytes: true})
	w.Write(fmt.Sprintf("$%d", len(w.slots)))
	return w
}

// ParamUses counts the placeholders of name bound by BindBytesParam (bytes) and by BindParam (other).
func (w *SQLWriter) ParamUses(name string) (bytes, other int) {
	for _, s := range w.slots {
		switch {
		case s.name != name:
		case s.bytes:
			bytes++
		default:
			other++
		}
	}
	return bytes, other
}

// Args returns the bind values in $N order. Panics if the statement has a BindParam placeholder.
func (w *SQLWriter) Args() []any {
	out := make([]any, len(w.slots))
	for i, s := range w.slots {
		if s.name != "" {
			panic(fmt.Sprintf("writer: Args() called on a statement with an unresolved $param %q — use ResolveArgs", s.name))
		}
		out[i] = s.value
	}
	return out
}

// ParamNames returns every distinct name reserved via BindParam, in first-occurrence order.
func (w *SQLWriter) ParamNames() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range w.slots {
		if s.name == "" || seen[s.name] {
			continue
		}
		seen[s.name] = true
		out = append(out, s.name)
	}
	return out
}

// ResolveArgs returns one request's args, named slots taken from paramValues, which the caller has already
// validated and defaulted. A missing name is an error.
func (w *SQLWriter) ResolveArgs(paramValues map[string]any) ([]any, error) {
	out := make([]any, len(w.slots))
	for i, s := range w.slots {
		if s.name == "" {
			out[i] = s.value
			continue
		}
		v, ok := paramValues[s.name]
		if !ok {
			return nil, oops.With("param", s.name).Errorf("writer: no value supplied for $param %q", s.name)
		}
		if s.json && v != nil {
			// pgx sends a string to json as the JSON text itself, so "abc" would not parse
			b, err := json.Marshal(v)
			if err != nil {
				return nil, oops.With("param", s.name).Wrapf(err, "writer: $param %q as JSON", s.name)
			}
			v = b
		}
		out[i] = v
	}
	return out, nil
}

var validUnquotedSQLId = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// sqlReservedKeywords are Postgres's fully reserved keywords, never valid unquoted.
var sqlReservedKeywords = map[string]bool{
	"all": true, "analyse": true, "analyze": true, "and": true, "any": true,
	"array": true, "as": true, "asc": true, "asymmetric": true, "both": true,
	"case": true, "cast": true, "check": true, "collate": true, "column": true,
	"constraint": true, "create": true, "current_catalog": true, "current_date": true,
	"current_role": true, "current_time": true, "current_timestamp": true,
	"current_user": true, "default": true, "deferrable": true, "desc": true,
	"distinct": true, "do": true, "else": true, "end": true, "except": true,
	"false": true, "fetch": true, "for": true, "foreign": true, "from": true,
	"grant": true, "group": true, "having": true, "in": true, "initially": true,
	"intersect": true, "into": true, "lateral": true, "leading": true, "limit": true,
	"localtime": true, "localtimestamp": true, "not": true, "null": true,
	"offset": true, "on": true, "only": true, "or": true, "order": true,
	"placing": true, "primary": true, "references": true, "returning": true,
	"select": true, "session_user": true, "some": true, "symmetric": true,
	"table": true, "then": true, "to": true, "trailing": true, "true": true,
	"union": true, "unique": true, "user": true, "using": true, "variadic": true,
	"when": true, "where": true, "window": true, "with": true,
}

// EscapeSQLId quotes each dot-separated part of id only when needed : hotel.rooms, hotel."order".
func EscapeSQLId(id string) string {
	parts := strings.Split(id, ".")
	for i, p := range parts {
		parts[i] = escapeSQLIdPart(p)
	}
	return strings.Join(parts, ".")
}

func escapeSQLIdPart(p string) string {
	if p != "" && validUnquotedSQLId.MatchString(p) && !sqlReservedKeywords[p] {
		return p
	}
	return `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
}

// Id writes an escaped identifier.
func (w *SQLWriter) Id(id string) *SQLWriter {
	w.Write(EscapeSQLId(id))
	return w
}
