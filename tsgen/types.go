package tsgen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/rel-server/rel/declcomment"
	"github.com/rel-server/rel/pg"
)

// baseScalarTypes maps a built-in pg_catalog type name to its TS equivalent. Anything not listed here falls
// back to `unknown`.
//
// Types whose wire value `Date`/`number` would misdescribe (a string, or a number JSON.parse rounds) map to a
// branded string type from pg_values.ts.
var baseScalarTypes = map[string]string{
	"text":    "string",
	"varchar": "string",
	"bpchar":  "string",
	"char":    "string",
	"name":    "string",
	"citext":  "string",
	"uuid":    "string",
	// bytea arrives as a hex string ("\\x0123abcd").
	"bytea":  "Bytea",
	"bool":   "boolean",
	"int2":   "number",
	"int4":   "number",
	"float4": "number",
	"float8": "number",
	"oid":    "number",
	"json":   "unknown",
	"jsonb":  "unknown",

	// JSON.parse would round these : the query compiler casts both to text.
	"int8":    "Int8",
	"numeric": "PgNumeric",
	// money arrives as a locale-formatted string ("$10.50").
	"money": "Money",

	// Temporal types arrive as strings.
	"date":        "PgDate",
	"time":        "PgTime",
	"timetz":      "PgTimetz",
	"timestamp":   "Timestamp",
	"timestamptz": "Timestamptz",
	"interval":    "Interval",

	// Range types, matched by name.
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

	// Network, bit string, geometric, text search and misc types : branded so they are not mixed with plain strings.
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

// typeCollector accumulates the Type__ declarations met while walking column/argument types, once per
// "schema.name".
type typeCollector struct {
	// ids is set by GenerateSchema once the target relations are known ; nil leaves every column untouched.
	ids *idResolver
	// decls resolves declared types ; imports collects the names they import.
	decls   declSource
	imports *importSet
	// jsonWire types a CBOR value never : a JSON client can neither read nor write one.
	jsonWire bool
	seen     map[string]bool
	order    []string
	out      map[string]string
}

func newTypeCollector() *typeCollector {
	return &typeCollector{seen: map[string]bool{}, out: map[string]string{}, imports: &importSet{}}
}

func (tc *typeCollector) declarations() string {
	sort.Strings(tc.order)
	var b strings.Builder
	for _, key := range tc.order {
		b.WriteString(tc.out[key])
		b.WriteString("\n")
	}
	return b.String()
}

// tsTypeExpr returns the TS type expression for t, registering every composite/enum/domain type it references
// into tc.
func tsTypeExpr(t *pg.Type, tc *typeCollector) string {
	if t == nil {
		return "unknown"
	}

	if t.IsArray() {
		return tsTypeExpr(t.ElementType, tc) + "[]"
	}

	if t.IsDomain() {
		return namedTypeExpr(t, tc, func() string {
			if tc.cborUnreadable(t) {
				return "never"
			}
			if d, _ := tc.decls.typ(t); d.Type != "" {
				return declaredTypeExpr(d, tc)
			}
			if t.CBOR && !t.BaseType.CBOR {
				return "unknown" // a cbor domain over bytea holds a CBOR value, not bytes
			}
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

	if t.CBOR { // postcbor's own type
		if tc.jsonWire {
			return "never"
		}
		return "unknown"
	}
	if ts, ok := baseScalarTypes[t.PgIdentifier.Name]; ok {
		return ts
	}
	return "unknown"
}

// namedTypeExpr registers a Type__<Schema>__<Name> declaration for t once, and returns its name. body renders
// the interface body or alias target, only on the first registration.
func namedTypeExpr(t *pg.Type, tc *typeCollector, body func() string) string {
	key := t.PgIdentifier.String()
	name := typeInterfaceName(t.PgIdentifier.Schema, t.PgIdentifier.Name)
	if tc.seen[key] {
		return name
	}
	// Marked seen before body() runs : a composite type may reference itself.
	tc.seen[key] = true
	tc.order = append(tc.order, key)

	d, _ := tc.decls.typ(t)
	comment := docComment(d.Comment, "")
	rhs := body()
	var decl string
	switch {
	case t.IsComposite():
		decl = fmt.Sprintf("%sinterface %s {\n%s}\n", comment, name, rhs)
	default:
		decl = fmt.Sprintf("%stype %s = %s\n", comment, name, rhs)
	}
	tc.out[key] = decl
	return name
}

// docComment renders s as a /** ... */ line, indented by indent ; empty when s is empty. A `*/` in s would end
// the comment early : it is written `*\/`.
func docComment(s, indent string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", " ")
	return indent + "/** " + strings.ReplaceAll(s, "*/", "*\\/") + " */\n"
}

// columnTSType is a column's read type, with `| null` unless the column is really not null. A declared type
// replaces the column type's own ; a CBOR value under JSON is never, null included.
func columnTSType(c *pg.Column, d declcomment.TypeDecl, tc *typeCollector) string {
	if tc.cborUnreadable(c.Type) {
		tsTypeExpr(c.Type, tc) // still registers the Type__ declarations it names
		return "never"
	}
	expr := tsTypeExpr(c.Type, tc)
	if d.Type != "" {
		expr = declaredTypeExpr(d, tc)
		if !c.IsReallyNotNull() && d.From == "" && !declcomment.IsIdentifier(d.Type) {
			expr = "(" + expr + ")" // `() => void | null` would read as a function returning null
		}
	}
	if id := tc.ids.typeExpr(c); id != "" {
		expr = id
	}
	if c.IsReallyNotNull() {
		return expr
	}
	return expr + " | null"
}

// relationColumnsBlock renders the interface body of a Table__/View__/composite Type__, one line per column.
func relationColumnsBlock(r *pg.Relation, tc *typeCollector) string {
	var b strings.Builder
	for _, c := range r.Columns {
		d, _ := tc.decls.column(r, c)
		b.WriteString(docComment(d.Comment, "  "))
		fmt.Fprintf(&b, "  %s: %s\n", identifierField(c.Name), columnTSType(c, d, tc))
	}
	return b.String()
}

// identifierField quotes a column name only when it isn't a valid bare TS identifier.
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
