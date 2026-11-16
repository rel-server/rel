package tsgen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/wellknown"
)

// Options configures GenerateSchema/GenerateDatabaseTS.
type Options struct {
	// Schemas is the resolved whitelist (see Resolve) ; empty means every schema but pg_catalog and the Blacklist.
	Schemas []string

	Blacklist config.Blacklist

	// Codec is the codec options of the generated database.ts (ParseCodec) ; the zero value is plain JSON.
	Codec Codec
}

func (o Options) schemaAllowed(schema string) bool {
	if schema == "pg_catalog" {
		return false
	}
	if len(o.Schemas) == 0 {
		return true
	}
	for _, s := range o.Schemas {
		if s == schema {
			return true
		}
	}
	return false
}

// targetRelation reports whether r gets a Table__/View__ : a real table or view, in an allowed schema, not
// blacklisted.
func (o Options) targetRelation(r *pg.Relation) bool {
	if r.IsSynthetic || r.IsStandaloneCompositeType {
		return false
	}
	if !o.schemaAllowed(r.Identifier.Schema) {
		return false
	}
	return !o.Blacklist.IsRelationBlacklisted(r.Identifier.Schema, r.Identifier.Name)
}

func (o Options) targetFunction(f *pg.Function) bool {
	if !f.IsPlainFunction() {
		return false
	}
	if !o.schemaAllowed(f.Identifier.Schema) {
		return false
	}
	return !o.Blacklist.IsFunctionBlacklisted(f.Identifier.Schema, f.Identifier.Name)
}

// GenerateSchema renders the schema section of database.ts, from db filtered by opts.
func GenerateSchema(db *pg.DbInfos, opts Options, wkReg *wellknown.Registry) string {
	tc := newTypeCollector()

	relations := targetRelations(db, opts)
	fns := targetFunctions(db, opts)
	allowed := map[*pg.Relation]bool{}
	for _, r := range relations {
		allowed[r] = true
	}
	tc.ids = newIdResolver(db, relations, tc)

	relInterfaces := renderRelationInterfaces(relations, tc)
	relationsMap := renderRelationsMap(relations)
	requiredColumns := renderRequiredColumns(relations)
	relationships := renderRelationships(relations, allowed, tc)
	computedProperties := renderComputedProperties(relations, opts.Blacklist, tc)
	functions := renderFunctions(fns, allowed, tc)
	functionsByName := renderFunctionsByName(fns, db.SearchPath)

	var b strings.Builder
	// EmptyObject types an empty object shape (no-argument `args`, zero-column relation). The lookup interfaces
	// (Functions, Wellknowns...) stay literal empty interfaces instead : their `keyof` must be `never`, not `string`.
	b.WriteString("type EmptyObject = Record<string, never>\n\n")
	b.WriteString(tc.declarations())
	b.WriteString(tc.ids.declaration())
	b.WriteString(relInterfaces)
	b.WriteString(relationsMap)
	b.WriteString(requiredColumns)
	b.WriteString(relationships)
	b.WriteString(computedProperties)
	b.WriteString(functions)
	b.WriteString(functionsByName)
	b.WriteString(renderWellknowns(wkReg))
	return b.String()
}

// wellknownConstIdent derives a TS identifier for a well-known query's const from its name, which may hold any
// character ; the Wellknowns key stays the quoted name.
func wellknownConstIdent(name string) string {
	var b strings.Builder
	b.WriteString("__wellknown_")
	for i, r := range name {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
		isDigit := r >= '0' && r <= '9'
		if isLetter || (i > 0 && isDigit) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	b.WriteString("_query")
	return b.String()
}

// renderWellknowns renders the Wellknowns section : each query's raw JSON is embedded as a `const ... as const`
// literal, and shapes.ts infers the row shape from it, since the literal carries its own relation/function.
// Always emitted, even empty : shapes.ts references it unconditionally.
func renderWellknowns(wkReg *wellknown.Registry) string {
	entries := wkReg.All()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	var consts strings.Builder
	var iface strings.Builder
	if len(entries) == 0 {
		iface.WriteString("// biome-ignore lint/suspicious/noEmptyInterface: always emitted, see this function's own doc comment\n")
	}
	iface.WriteString("export interface Wellknowns {\n")
	for _, c := range entries {
		ident := wellknownConstIdent(c.Name)
		fmt.Fprintf(&consts, "const %s = %s as const\n", ident, string(c.QueryRaw))

		paramNames := make([]string, 0, len(c.Params))
		for name := range c.Params {
			paramNames = append(paramNames, name)
		}
		sort.Strings(paramNames)
		paramFields := make([]string, len(paramNames))
		for i, name := range paramNames {
			p := c.Params[name]
			opt := ""
			if p.HasDefault {
				opt = "?"
			}
			paramFields[i] = fmt.Sprintf("%s%s: %s", identifierField(name), opt, wellknown.TSTypeForParam(p.Type))
		}
		paramsType := "EmptyObject"
		if len(paramFields) > 0 {
			paramsType = "{ " + strings.Join(paramFields, "; ") + " }"
		}

		fmt.Fprintf(&iface, "  %s: {\n", strconv.Quote(c.Name))
		fmt.Fprintf(&iface, "    params: %s\n", paramsType)
		fmt.Fprintf(&iface, "    shape: RootShapeFromLiteralQuery<typeof %s>\n", ident)
		fmt.Fprintf(&iface, "    write_shape: WriteShapeFromRelationQuery<typeof %s, ResolveModel<typeof %s>>[]\n", ident, ident)
		iface.WriteString("  }\n")
	}
	iface.WriteString("}\n\n")

	return consts.String() + "\n" + iface.String()
}

func targetRelations(db *pg.DbInfos, opts Options) []*pg.Relation {
	var out []*pg.Relation
	for _, r := range db.Relations {
		if opts.targetRelation(r) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Identifier.Schema != out[j].Identifier.Schema {
			return out[i].Identifier.Schema < out[j].Identifier.Schema
		}
		return out[i].Identifier.Name < out[j].Identifier.Name
	})
	return out
}

func targetFunctions(db *pg.DbInfos, opts Options) []*pg.Function {
	var out []*pg.Function
	for _, f := range db.Functions {
		if opts.targetFunction(f) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Identifier.Schema != out[j].Identifier.Schema {
			return out[i].Identifier.Schema < out[j].Identifier.Schema
		}
		return out[i].Identifier.Name < out[j].Identifier.Name
	})
	return out
}

func renderRelationInterfaces(relations []*pg.Relation, tc *typeCollector) string {
	var b strings.Builder
	for _, r := range relations {
		name := relationInterfaceName(r.Identifier.Schema, r.Identifier.Name, r.IsViewLike())
		b.WriteString(docComment(r.Comment, ""))
		body := relationColumnsBlock(r, tc)
		if body == "" {
			// A zero-column relation (CREATE TABLE t()) is an alias to EmptyObject.
			fmt.Fprintf(&b, "type %s = EmptyObject\n\n", name)
			continue
		}
		fmt.Fprintf(&b, "interface %s {\n%s}\n\n", name, body)
	}
	return b.String()
}

func renderRelationsMap(relations []*pg.Relation) string {
	var b strings.Builder
	b.WriteString("export interface Relations {\n")
	for _, r := range relations {
		key := relationKey(r.Identifier.Schema, r.Identifier.Name)
		name := relationInterfaceName(r.Identifier.Schema, r.Identifier.Name, r.IsViewLike())
		fmt.Fprintf(&b, "  %s: %s\n", strconv.Quote(key), name)
	}
	b.WriteString("}\n\n")
	return b.String()
}

// isRequiredColumn reports whether an insert must give c a value : not null, no default, not identity or
// generated. Whether the column is writable is not considered.
func isRequiredColumn(c *pg.Column) bool {
	return c.IsReallyNotNull() && c.DefaultExpression == "" && !c.IsIdentity && !c.IsGenerated
}

// renderRequiredColumns renders RequiredColumns : per target relation, the columns a write must supply, which
// shapes.ts makes mandatory in the write shape. `never` when there is none : every relation key must be present.
func renderRequiredColumns(relations []*pg.Relation) string {
	var b strings.Builder
	b.WriteString("export interface RequiredColumns {\n")
	for _, r := range relations {
		key := relationKey(r.Identifier.Schema, r.Identifier.Name)
		var required []string
		for _, c := range r.Columns {
			if isRequiredColumn(c) {
				required = append(required, c.Name)
			}
		}
		if len(required) == 0 {
			fmt.Fprintf(&b, "  %s: never\n", strconv.Quote(key))
			continue
		}
		sort.Strings(required)
		lits := make([]string, len(required))
		for i, n := range required {
			lits[i] = strconv.Quote(n)
		}
		fmt.Fprintf(&b, "  %s: %s\n", strconv.Quote(key), strings.Join(lits, " | "))
	}
	b.WriteString("}\n\n")
	return b.String()
}

// relationshipVariant is one foreign key entry under Relationships[key] ; the shortcut's marker (`>`/`<`/`*`)
// carries its cardinality.
type relationshipVariant struct {
	key      string // owning key this variant is filed under
	shortcut string
	relation string // the OTHER side's Table__/View__ name
	reason   string // IneligibleRelationships only : why join() can't use it
}

// renderRelationships renders two variants per foreign key, one per direction. A foreign key whose columns are
// not indexed can't be joined either way : its variants go to IneligibleRelationships with the reason, so join()
// can say why. Both interfaces are always emitted, even empty, since querier.ts indexes them unconditionally.
func renderRelationships(relations []*pg.Relation, allowed map[*pg.Relation]bool, tc *typeCollector) string {
	eligible := newVariantSet()
	ineligible := newVariantSet()

	for _, owner := range relations {
		for _, c := range owner.OutgoingForeignKeys {
			referenced := c.OtherRelation()
			if referenced == nil || !allowed[referenced] {
				continue
			}
			ownerCols := columnNames(c.Columns)

			pairs := make([]string, len(c.Columns))
			for i := range c.Columns {
				pairs[i] = c.Target.Columns[i].Name + ":" + c.Columns[i].Name
			}
			pairStr := strings.Join(pairs, ",")

			ownerName := relationInterfaceName(owner.Identifier.Schema, owner.Identifier.Name, owner.IsViewLike())
			referencedName := relationInterfaceName(referenced.Identifier.Schema, referenced.Identifier.Name, referenced.IsViewLike())
			ownerKey := relationKey(owner.Identifier.Schema, owner.Identifier.Name)
			referencedKey := relationKey(referenced.Identifier.Schema, referenced.Identifier.Name)

			// `>` outgoing is always to-one ; incoming is `<` (to-one, a unique reverse FK) or `*` (to-many).
			incomingMarker := "*"
			if owner.FindUniqueConstraint(ownerCols) != nil {
				incomingMarker = "<"
			}

			outgoing := relationshipVariant{key: ownerKey, shortcut: referencedKey + ">" + pairStr, relation: referencedName}
			incoming := relationshipVariant{key: referencedKey, shortcut: ownerKey + incomingMarker + pairStr, relation: ownerName}

			if owner.IsIndexed(ownerCols) {
				eligible.add(outgoing)
				eligible.add(incoming)
				continue
			}
			reason := fmt.Sprintf("%s (%s) has no index, so %s and %s can't be joined: add an index on those columns",
				ownerKey, strings.Join(ownerCols, ", "), ownerKey, referencedKey)
			outgoing.reason, incoming.reason = reason, reason
			ineligible.add(outgoing)
			ineligible.add(incoming)
		}
	}

	return renderVariantInterface("Relationships", eligible, false) +
		renderVariantInterface("IneligibleRelationships", ineligible, true)
}

// variantSet collects relationshipVariants by owning key, remembering first-seen key order.
type variantSet struct {
	byKey map[string][]relationshipVariant
	order []string
}

func newVariantSet() *variantSet { return &variantSet{byKey: map[string][]relationshipVariant{}} }

func (vs *variantSet) add(v relationshipVariant) {
	if _, seen := vs.byKey[v.key]; !seen {
		vs.order = append(vs.order, v.key)
	}
	vs.byKey[v.key] = append(vs.byKey[v.key], v)
}

func renderVariantInterface(name string, vs *variantSet, withReason bool) string {
	var b strings.Builder
	if len(vs.order) == 0 {
		b.WriteString("// biome-ignore lint/suspicious/noEmptyInterface: always emitted, see renderRelationships's doc comment\n")
		fmt.Fprintf(&b, "export interface %s {}\n\n", name)
		return b.String()
	}
	sort.Strings(vs.order)
	fmt.Fprintf(&b, "export interface %s {\n", name)
	for _, key := range vs.order {
		variants := vs.byKey[key]
		sort.Slice(variants, func(i, j int) bool { return variants[i].shortcut < variants[j].shortcut })
		fmt.Fprintf(&b, "  %s:", strconv.Quote(key))
		if len(variants) == 1 {
			b.WriteString(" " + variantLiteral(variants[0], withReason) + "\n")
			continue
		}
		b.WriteString("\n")
		for _, v := range variants {
			fmt.Fprintf(&b, "    | %s\n", variantLiteral(v, withReason))
		}
	}
	b.WriteString("}\n\n")
	return b.String()
}

func variantLiteral(v relationshipVariant, withReason bool) string {
	if withReason {
		return fmt.Sprintf("{ shortcut: %s; reason: %s }", strconv.Quote(v.shortcut), strconv.Quote(v.reason))
	}
	return fmt.Sprintf("{ shortcut: %s; relation: %s }", strconv.Quote(v.shortcut), v.relation)
}

func columnNames(columns []*pg.Column) []string {
	names := make([]string, len(columns))
	for i, c := range columns {
		names[i] = c.Name
	}
	return names
}

// bareNameWinners maps each bare function name to the "schema.name" key an unqualified call resolves to : the
// first schema of searchPath holding that name. A name with no candidate on the path is omitted. Argument types
// are ignored, so an earlier schema wins even when its overloads don't fit the call.
func bareNameWinners(fns []*pg.Function, searchPath []string) map[string]string {
	pathIndex := make(map[string]int, len(searchPath))
	for i, s := range searchPath {
		pathIndex[s] = i
	}

	type candidate struct {
		schema string
		rank   int
	}
	best := map[string]candidate{}
	for _, f := range fns {
		rank, onPath := pathIndex[f.Identifier.Schema]
		if !onPath {
			continue
		}
		name := f.Identifier.Name
		if cur, ok := best[name]; !ok || rank < cur.rank {
			best[name] = candidate{schema: f.Identifier.Schema, rank: rank}
		}
	}

	winners := make(map[string]string, len(best))
	for name, c := range best {
		winners[name] = relationKey(c.schema, name)
	}
	return winners
}

// renderFunctionsByName renders FunctionsByName, which types `["call", "func_name", ...]` ; each entry indexes
// into Functions, so overloads carry through. Always emitted, even empty : shapes.ts references it unconditionally.
func renderFunctionsByName(fns []*pg.Function, searchPath []string) string {
	winners := bareNameWinners(fns, searchPath)
	names := make([]string, 0, len(winners))
	for name := range winners {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	if len(names) == 0 {
		b.WriteString("// biome-ignore lint/suspicious/noEmptyInterface: always emitted, see this function's own doc comment\n")
	}
	b.WriteString("export interface FunctionsByName {\n")
	for _, name := range names {
		fmt.Fprintf(&b, "  %s: Functions[%s]\n", identifierField(name), strconv.Quote(winners[name]))
	}
	b.WriteString("}\n\n")
	return b.String()
}

// renderComputedProperties lists each relation's computed fields and their return types, minus blacklisted
// functions, for discovery only : a `["call", ...]` is typed through FunctionsByName/Functions.
func renderComputedProperties(relations []*pg.Relation, bl config.Blacklist, tc *typeCollector) string {
	byRelation := map[*pg.Relation]map[string]string{}
	for _, rel := range relations {
		for name, f := range rel.ComputedFields {
			if bl.IsFunctionBlacklisted(f.Identifier.Schema, f.Identifier.Name) {
				continue
			}
			ts := tsTypeExpr(f.ReturnType, tc)
			if f.ReturnsSet {
				ts += "[]"
			}
			if byRelation[rel] == nil {
				byRelation[rel] = map[string]string{}
			}
			byRelation[rel][name] = ts
		}
	}

	if len(byRelation) == 0 {
		return ""
	}

	var relOrder []*pg.Relation
	for r := range byRelation {
		relOrder = append(relOrder, r)
	}
	sort.Slice(relOrder, func(i, j int) bool {
		if relOrder[i].Identifier.Schema != relOrder[j].Identifier.Schema {
			return relOrder[i].Identifier.Schema < relOrder[j].Identifier.Schema
		}
		return relOrder[i].Identifier.Name < relOrder[j].Identifier.Name
	})

	var interfaces strings.Builder
	var mapBody strings.Builder
	mapBody.WriteString("export interface ComputedProperties {\n")
	for _, r := range relOrder {
		props := byRelation[r]
		names := make([]string, 0, len(props))
		for n := range props {
			names = append(names, n)
		}
		sort.Strings(names)

		compName := "Computed__" + pascalCase(r.Identifier.Schema) + "__" + pascalCase(r.Identifier.Name)
		fmt.Fprintf(&interfaces, "interface %s {\n", compName)
		for _, n := range names {
			fmt.Fprintf(&interfaces, "  %s: %s\n", identifierField(n), props[n])
		}
		interfaces.WriteString("}\n\n")

		key := relationKey(r.Identifier.Schema, r.Identifier.Name)
		fmt.Fprintf(&mapBody, "  %s: %s\n", strconv.Quote(key), compName)
	}
	mapBody.WriteString("}\n\n")

	return interfaces.String() + mapBody.String()
}

// renderFunctions renders the Functions interface. Always emitted, even empty : shapes.ts references it
// unconditionally.
func renderFunctions(fns []*pg.Function, allowed map[*pg.Relation]bool, tc *typeCollector) string {
	// Overloads share a key : each becomes a union variant.
	byKey := map[string][]*pg.Function{}
	var order []string
	for _, f := range fns {
		key := relationKey(f.Identifier.Schema, f.Identifier.Name)
		if _, seen := byKey[key]; !seen {
			order = append(order, key)
		}
		byKey[key] = append(byKey[key], f)
	}

	var b strings.Builder
	if len(order) == 0 {
		b.WriteString("// biome-ignore lint/suspicious/noEmptyInterface: always emitted, see this function's own doc comment\n")
	}
	b.WriteString("export interface Functions {\n")
	for _, key := range order {
		overloads := byKey[key]
		fmt.Fprintf(&b, "  %s:", strconv.Quote(key))
		if len(overloads) == 1 {
			b.WriteString(" " + functionShapeLiteral(overloads[0], allowed, tc) + "\n")
			continue
		}
		b.WriteString("\n")
		for _, f := range overloads {
			fmt.Fprintf(&b, "    | %s\n", functionShapeLiteral(f, allowed, tc))
		}
	}
	b.WriteString("}\n\n")
	return b.String()
}

// functionShapeLiteral renders one function's { positional_args; args; relation?; returns } object literal.
func functionShapeLiteral(f *pg.Function, allowed map[*pg.Relation]bool, tc *typeCollector) string {
	var b strings.Builder
	b.WriteString("{\n")
	renderFunctionArgs(&b, f, tc)
	renderFunctionReturns(&b, f, allowed, tc)
	b.WriteString("  }")
	return b.String()
}

// inputArg is one IN/INOUT/VARIADIC argument.
type inputArg struct {
	name     string
	tsType   string
	optional bool
}

func inputArgs(f *pg.Function, tc *typeCollector) []inputArg {
	var in []*pg.FunctionArgument
	for i := range f.Arguments {
		a := &f.Arguments[i]
		if a.IsIn() || a.IsInOut() || a.IsVariadic() {
			in = append(in, a)
		}
	}
	out := make([]inputArg, len(in))
	// PgNargsDefaults counts trailing input args with a default.
	firstOptional := len(in) - f.PgNargsDefaults
	for i, a := range in {
		name := a.Name
		if name == "" {
			name = fmt.Sprintf("arg%d", i+1)
		}
		out[i] = inputArg{name: name, tsType: tsTypeExpr(a.Type, tc), optional: i >= firstOptional}
	}
	return out
}

func renderFunctionArgs(b *strings.Builder, f *pg.Function, tc *typeCollector) {
	args := inputArgs(f, tc)

	tuple := make([]string, len(args))
	obj := make([]string, len(args))
	for i, a := range args {
		opt := ""
		if a.optional {
			opt = "?"
		}
		tuple[i] = fmt.Sprintf("%s%s: %s", identifierField(a.name), opt, a.tsType)
		obj[i] = fmt.Sprintf("%s%s: %s", identifierField(a.name), opt, a.tsType)
	}
	fmt.Fprintf(b, "    positional_args: [%s]\n", strings.Join(tuple, ", "))
	argsType := "EmptyObject"
	if len(obj) > 0 {
		argsType = "{ " + strings.Join(obj, "; ") + " }"
	}
	fmt.Fprintf(b, "    args: %s\n", argsType)
}

// renderFunctionReturns renders `relation`/`returns` : `relation` only for a set-returning function of a target
// relation ; a RETURNS TABLE/OUT-parameter function gets an inline object shape.
func renderFunctionReturns(b *strings.Builder, f *pg.Function, allowed map[*pg.Relation]bool, tc *typeCollector) {
	if f.ReturnsSet {
		if rel := f.ReturnType.CompositeRelation(); rel != nil && allowed[rel] {
			name := relationInterfaceName(rel.Identifier.Schema, rel.Identifier.Name, rel.IsViewLike())
			fmt.Fprintf(b, "    relation: %s\n", name)
			fmt.Fprintf(b, "    returns: %s[]\n", name)
			return
		}
		if f.RecordRelation != nil {
			fmt.Fprintf(b, "    returns: %s[]\n", recordRelationTypeExpr(f.RecordRelation, tc))
			return
		}
		fmt.Fprintf(b, "    returns: %s[]\n", tsTypeExpr(f.ReturnType, tc))
		return
	}
	if f.RecordRelation != nil {
		fmt.Fprintf(b, "    returns: %s\n", recordRelationTypeExpr(f.RecordRelation, tc))
		return
	}
	fmt.Fprintf(b, "    returns: %s\n", tsTypeExpr(f.ReturnType, tc))
}

// recordRelationTypeExpr renders a function's RecordRelation as an inline object type.
func recordRelationTypeExpr(r *pg.Relation, tc *typeCollector) string {
	body := relationColumnsBlock(r, tc)
	if body == "" {
		return "EmptyObject"
	}
	return "{\n" + indentBlock(body, "    ") + "    }"
}

// indentBlock prepends prefix to every non-empty line of s.
func indentBlock(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l == "" {
			continue
		}
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n") + "\n"
}
