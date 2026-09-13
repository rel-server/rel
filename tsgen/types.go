package tsgen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/rel-server/rel/pg"
)

// baseScalarTypes maps a built-in pg_catalog type name to its TS equivalent. Anything not listed here falls
// back to `unknown`.
//
// The temporal/precision-sensitive/other types below map to a branded string type from pg_values.ts
// (docs/content/typescript/index.md ## Typed wire values), not to `Date`/`number`/`unknown` — each of those
// lies about the actual JSON wire value in some way (a plain string typed `Date`, or a value that silently
// loses precision through `JSON.parse`) ; see that doc for the full reasoning per type and the accessors
// (pg_values.ts) that derive a richer client-side value from each branded string without losing type safety.
var baseScalarTypes = map[string]string{
	"text":    "string",
	"varchar": "string",
	"bpchar":  "string",
	"char":    "string",
	"name":    "string",
	"citext":  "string",
	"uuid":    "string",
	// bytea's row_to_json wire format is NOT raw bytes or base64 : Postgres
	// renders it via its own bytea_output setting (default "hex"), giving a
	// JSON string shaped like "\\x0123abcd" (backslash-x, then hex digits) —
	// a real `string` on the wire, but one the caller must still decode
	// (strip the "\x" prefix, Buffer.from(hex, "hex")/equivalent) before
	// it's usable as binary content. No separate Type__ marks this ; nothing
	// in the introspected schema JSON distinguishes "true text" from
	// "bytea-shaped string" once it's collapsed to `string` here.
	"bytea":  "string",
	"bool":   "boolean",
	"int2":   "number",
	"int4":   "number",
	"float4": "number",
	"float8": "number",
	"json":   "unknown",
	"jsonb":  "unknown",

	// Precision-sensitive : Postgres emits these as bare, unquoted JSON numerals, so JSON.parse rounds each to
	// the nearest float64 before any client code runs. The query compiler casts both to text (query/sql.go) ;
	// pg_values.ts's Int8/PgNumeric are the branded string types that land in.
	"int8":    "Int8",
	"numeric": "PgNumeric",
	// money renders as a locale-formatted, quoted string ("$10.50") today already — mapping it to `number` was
	// already a lie before this table's other corrections, not something the query compiler needs to change.
	"money": "Money",

	// Temporal : all arrive as plain strings today ; `date`/`time`/`timetz` are also currently mistyped as
	// `Date` even though nothing about them is unsafe to read (only `timestamp`, no time zone, actually has an
	// ambiguity to caveat).
	"date":        "PgDate",
	"time":        "PgTime",
	"timetz":      "PgTimetz",
	"timestamp":   "Timestamp",
	"timestamptz": "Timestamptz",
	"interval":    "Interval",

	// Range types : matched by name like every other entry in this map, no structural range introspection
	// needed — see docs/content/typescript/index.md ## Typed wire values ## Ranges for why.
	"int4range":      "Int4Range",
	"int8range":      "Int8Range",
	"numrange":       "NumRange",
	"daterange":      "DateRange",
	"tsrange":        "TsRange",
	"tstzrange":      "TstzRange",
	"int4multirange": "Int4MultiRange",
	"int8multirange": "Int8MultiRange",
	"nummultirange":  "NumMultiRange",
	"datemultirange": "DateMultiRange",
	"tsmultirange":   "TsMultiRange",
	"tstzmultirange": "TstzMultiRange",

	// Network, bit string, geometric, text search, and misc types (docs/content/typescript/index.md ## Typed
	// wire values) : branded for hover clarity and to stop them being confused with an unrelated plain string,
	// most with no accessor beyond the branded type itself (pg_values.ts's own accessor helpers say which).
	"inet":     "Inet",
	"cidr":     "Cidr",
	"macaddr":  "MacAddr",
	"macaddr8": "MacAddr8",
	"bit":      "PgBit",
	"varbit":   "PgVarbit",
	"point":    "PgPoint",
	"line":     "PgLine",
	"lseg":     "PgLseg",
	"box":      "PgBox",
	"path":     "PgPath",
	"polygon":  "PgPolygon",
	"circle":   "PgCircle",
	"tsvector": "TsVector",
	"tsquery":  "TsQuery",
	"xml":      "Xml",
	"pg_lsn":   "PgLsn",
}

// typeCollector accumulates the named (Type__) declarations discovered while
// walking column/argument types, de-duplicated by "schema.name" — a type
// referenced from two different columns must only be declared once.
type typeCollector struct {
	seen  map[string]bool
	order []string
	decls map[string]string
}

func newTypeCollector() *typeCollector {
	return &typeCollector{seen: map[string]bool{}, decls: map[string]string{}}
}

func (tc *typeCollector) declarations() string {
	sort.Strings(tc.order)
	var b strings.Builder
	for _, key := range tc.order {
		b.WriteString(tc.decls[key])
		b.WriteString("\n")
	}
	return b.String()
}

// tsTypeExpr returns the TS type expression a column/argument of type t
// should use, registering any composite/enum/domain type it (transitively)
// references into tc exactly once (specs/typescript.md ## Schema interfaces
// : "Type__<Schema>__<Name> ... one per introspected composite/enum/domain
// type referenced by a column or function argument").
func tsTypeExpr(t *pg.Type, tc *typeCollector) string {
	if t == nil {
		return "unknown"
	}

	if t.IsArray() {
		return tsTypeExpr(t.ElementType, tc) + "[]"
	}

	if t.IsDomain() {
		return namedTypeExpr(t, tc, func() string {
			return tsTypeExpr(t.BaseType, tc)
		})
	}

	if t.IsEnum() {
		return namedTypeExpr(t, tc, func() string {
			labels := make([]string, len(t.EnumLabels))
			for i, l := range t.EnumLabels {
				labels[i] = strconv.Quote(l)
			}
			return strings.Join(labels, " | ")
		})
	}

	if t.IsComposite() {
		return namedTypeExpr(t, tc, func() string {
			return relationColumnsBlock(t.Relation, tc)
		})
	}

	if ts, ok := baseScalarTypes[t.PgIdentifier.Name]; ok {
		return ts
	}
	return "unknown"
}

// namedTypeExpr registers a Type__<Schema>__<Name> declaration for t (once,
// via tc's own de-dup), then returns its name as the reference expression.
// body renders the right-hand side ("interface Name { ... }"'s body, or a
// type alias's target) ; called lazily, only on the first registration.
func namedTypeExpr(t *pg.Type, tc *typeCollector, body func() string) string {
	key := t.PgIdentifier.String()
	name := typeInterfaceName(t.PgIdentifier.Schema, t.PgIdentifier.Name)
	if tc.seen[key] {
		return name
	}
	// Marked seen BEFORE body() runs : a composite type whose own columns
	// reference itself (e.g. a self-referencing tree type) would otherwise recurse forever.
	tc.seen[key] = true
	tc.order = append(tc.order, key)

	comment := docComment(t.Comment, "")
	rhs := body()
	var decl string
	switch {
	case t.IsComposite():
		decl = fmt.Sprintf("%sinterface %s {\n%s}\n", comment, name, rhs)
	default:
		decl = fmt.Sprintf("%stype %s = %s\n", comment, name, rhs)
	}
	tc.decls[key] = decl
	return name
}

// docComment renders s as a /** ... */ block, indented by indent ; empty
// when s is empty, so a column/type with no COMMENT ON emits nothing.
func docComment(s, indent string) string {
	if s == "" {
		return ""
	}
	return indent + "/** " + strings.ReplaceAll(s, "\n", " ") + " */\n"
}

// columnTSType is a column's own read type : its scalar/array/named type
// expression, unioned with `null` unless the column is genuinely not-null
// (specs/typescript.md ## Schema interfaces ## Column typing).
func columnTSType(c *pg.Column, tc *typeCollector) string {
	expr := tsTypeExpr(c.Type, tc)
	if c.IsReallyNotNull() {
		return expr
	}
	return expr + " | null"
}

// relationColumnsBlock renders one Table__/View__/composite-Type__'s column
// list — the interface BODY, one line per column, shared by every caller
// that needs "the same column rules as Table__" (## Schema interfaces).
func relationColumnsBlock(r *pg.Relation, tc *typeCollector) string {
	var b strings.Builder
	for _, c := range r.Columns {
		b.WriteString(docComment(c.Comment, "  "))
		fmt.Fprintf(&b, "  %s: %s\n", identifierField(c.Name), columnTSType(c, tc))
	}
	return b.String()
}

// identifierField quotes a column name only when it isn't already a valid
// bare TS identifier — keeps generated output readable for the common case.
func identifierField(name string) string {
	if name == "" {
		return `""`
	}
	for i, r := range name {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || r == '$'
		isDigit := r >= '0' && r <= '9'
		if i == 0 && !isLetter {
			return strconv.Quote(name)
		}
		if i > 0 && !isLetter && !isDigit {
			return strconv.Quote(name)
		}
	}
	return name
}
