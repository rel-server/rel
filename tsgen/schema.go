package tsgen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/wellknown"
)

// Options configures GenerateSchema/GenerateDatabaseTS.
type Options struct {
	// Schemas is the already-resolved whitelist (specs/typescript.md ##
	// Endpoints' `schemas` query param intersected with `http.typescript.
	// schemas`, done by the caller) ; empty means every schema found, minus
	// pg_catalog and whatever Blacklist excludes.
	Schemas []string

	Blacklist config.Blacklist
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

// targetRelation reports whether r is a real, exportable Table__/View__
// candidate — not a function's own synthetic RecordRelation, not a bare
// composite type's backing pseudo-relation (that surfaces via Type__
// instead ; info_relation.go's IsStandaloneCompositeType), in an allowed,
// non-blacklisted schema.
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

// GenerateSchema renders specs/typescript.md ## database.ts ### File
// layout's Section 2 : Relations/Relationships/ComputedProperties/Functions/
// FunctionsByName/Wellknowns and the Table__/View__/Type__/Computed__
// interfaces they reference, from db filtered by opts.
func GenerateSchema(db *pg.DbInfos, opts Options, wkReg *wellknown.Registry) string {
	tc := newTypeCollector()

	relations := targetRelations(db, opts)
	fns := targetFunctions(db, opts)
	allowed := map[*pg.Relation]bool{}
	for _, r := range relations {
		allowed[r] = true
	}

	relInterfaces := renderRelationInterfaces(relations, tc)
	relationsMap := renderRelationsMap(relations)
	relationships := renderRelationships(relations, allowed, tc)
	computedProperties := renderComputedProperties(fns, allowed, tc)
	functions := renderFunctions(fns, allowed, tc)
	functionsByName := renderFunctionsByName(fns, db.SearchPath)

	var b strings.Builder
	// A bare `{}` type accepts anything non-null (biome's own noBannedTypes
	// rule flags it for exactly that reason) — EmptyObject is what an
	// actually-empty object shape (a zero-argument function's own `args`, a
	// zero-column relation/composite type — CREATE TABLE t() is legal
	// Postgres) generates instead. NOT used for Wellknowns/Functions/
	// FunctionsByName/Relations/Relationships/ComputedProperties themselves
	// even when they have no entries : those stay a literal empty interface
	// (biome-ignored) because their `keyof` is load-bearing (F extends keyof
	// Functions, Id extends keyof FunctionsByName, ...) — `keyof
	// Record<string, never>` is `string`, not `never`, which would make
	// every unrecognized name type-check as valid instead of correctly
	// falling back to DefaultRow/unknown.
	b.WriteString("type EmptyObject = Record<string, never>\n\n")
	b.WriteString(tc.declarations())
	b.WriteString(relInterfaces)
	b.WriteString(relationsMap)
	b.WriteString(relationships)
	b.WriteString(computedProperties)
	b.WriteString(functions)
	b.WriteString(functionsByName)
	b.WriteString(renderWellknowns(wkReg))
	return b.String()
}

// wellknownConstIdent derives a collision-safe TS identifier for a
// well-known's own raw-query const from its (arbitrary, possibly
// hyphenated/dotted) declared name — only this identifier needs sanitizing;
// Wellknowns' own property key stays the literal, quoted name.
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

// renderWellknowns is specs/typescript.md ## Wellknowns : rather than
// re-deriving a well-known query's row shape in Go (duplicating shapes.ts'
// own type-level inference), each compiled query's raw "query" JSON is
// embedded verbatim as a `const ... as const` literal and ShapeFromRelationQuery/
// WriteShapeFromRelationQuery (shapes.ts) infer its shape from that literal
// directly — the unconstrained entry points, not ShapeFromQuery/
// WriteShapeFromQuery, since `as const` makes every nested array/tuple
// readonly, which the constrained Q extends RelationQuery<...> signature
// rejects (RelationQuery's own where/select fields are typed as mutable
// tuples). Always emits the interface, even empty — same reasoning as
// Functions/FunctionsByName above.
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
		fmt.Fprintf(&iface, "    shape: ShapeFromRelationQuery<typeof %s, ResolveModel<typeof %s>>\n", ident, ident)
		fmt.Fprintf(&iface, "    write_shape: WriteShapeFromRelationQuery<typeof %s, ResolveModel<typeof %s>>\n", ident, ident)
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
		name := relationInterfaceName(r.Identifier.Schema, r.Identifier.Name, r.IsView)
		b.WriteString(docComment(r.Comment, ""))
		body := relationColumnsBlock(r, tc)
		if body == "" {
			// CREATE TABLE t() is legal Postgres — a zero-column relation
			// has no columns to interface-body, so it's a type alias to
			// EmptyObject instead of an empty `interface X {}` (this one
			// isn't a lookup map ; nothing needs its `keyof`).
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
		name := relationInterfaceName(r.Identifier.Schema, r.Identifier.Name, r.IsView)
		fmt.Fprintf(&b, "  %s: %s\n", strconv.Quote(key), name)
	}
	b.WriteString("}\n\n")
	return b.String()
}

// relationshipVariant is one FK-derived entry under Relationships[key] —
// specs/typescript.md ## Schema interfaces' "Relationships" paragraph.
type relationshipVariant struct {
	key      string // owning key this variant is filed under
	shortcut string
	unique   bool
	relation string // the OTHER side's Table__/View__ name
}

// renderRelationships walks every target relation's own OutgoingForeignKeys
// exactly once (a constraint belongs to exactly one relation's own list),
// producing up to two variants per eligible FK — see the type's own doc
// comment and specs/typescript.md ## Schema interfaces ## Relationships.
func renderRelationships(relations []*pg.Relation, allowed map[*pg.Relation]bool, tc *typeCollector) string {
	byKey := map[string][]relationshipVariant{}
	var order []string
	addVariant := func(v relationshipVariant) {
		if _, seen := byKey[v.key]; !seen {
			order = append(order, v.key)
		}
		byKey[v.key] = append(byKey[v.key], v)
	}

	for _, owner := range relations {
		for _, c := range owner.OutgoingForeignKeys {
			referenced := c.OtherRelation()
			if referenced == nil || !allowed[referenced] {
				continue
			}
			ownerCols := columnNames(c.Columns)
			// Eligibility (query.ts's own `on` doc comment, quoted verbatim
			// in specs/typescript.md ## Relationships) : the FK-holding
			// side's own columns must be indexed, or neither direction is a
			// legal join target.
			if !owner.IsIndexed(ownerCols) {
				continue
			}

			pairs := make([]string, len(c.Columns))
			for i := range c.Columns {
				pairs[i] = c.Target.Columns[i].Name + ":" + c.Columns[i].Name
			}
			pairStr := strings.Join(pairs, ",")

			ownerName := relationInterfaceName(owner.Identifier.Schema, owner.Identifier.Name, owner.IsView)
			referencedName := relationInterfaceName(referenced.Identifier.Schema, referenced.Identifier.Name, referenced.IsView)

			addVariant(relationshipVariant{
				key:      relationKey(owner.Identifier.Schema, owner.Identifier.Name),
				shortcut: relationKey(referenced.Identifier.Schema, referenced.Identifier.Name) + ">;" + pairStr,
				unique:   true, // an FK's target columns are always PK/UNIQUE-backed in Postgres
				relation: referencedName,
			})
			addVariant(relationshipVariant{
				key:      relationKey(referenced.Identifier.Schema, referenced.Identifier.Name),
				shortcut: relationKey(owner.Identifier.Schema, owner.Identifier.Name) + "<;" + pairStr,
				unique:   owner.FindUniqueConstraint(ownerCols) != nil,
				relation: ownerName,
			})
		}
	}

	if len(order) == 0 {
		return ""
	}
	sort.Strings(order)

	var b strings.Builder
	b.WriteString("export interface Relationships {\n")
	for _, key := range order {
		variants := byKey[key]
		sort.Slice(variants, func(i, j int) bool { return variants[i].shortcut < variants[j].shortcut })
		fmt.Fprintf(&b, "  %s:", strconv.Quote(key))
		if len(variants) == 1 {
			b.WriteString(" " + variantLiteral(variants[0]) + "\n")
			continue
		}
		b.WriteString("\n")
		for _, v := range variants {
			fmt.Fprintf(&b, "    | %s\n", variantLiteral(v))
		}
	}
	b.WriteString("}\n\n")
	return b.String()
}

func variantLiteral(v relationshipVariant) string {
	return fmt.Sprintf("{ shortcut: %s; unique: %t; relation: %s }", strconv.Quote(v.shortcut), v.unique, v.relation)
}

func columnNames(columns []*pg.Column) []string {
	names := make([]string, len(columns))
	for i, c := range columns {
		names[i] = c.Name
	}
	return names
}

// bareNameWinners maps each bare (unqualified) function name shared by one
// or more of fns to the ONE "schema.name" key an unqualified call to that
// name would actually resolve to at runtime — the same search_path-based
// winner Postgres itself picks for a bare `func_name(...)` call, including
// Postgres' own dot-syntax for computed columns (`t.func_name()`). A name
// whose only candidates all live outside searchPath resolves to nothing
// (matches Postgres : nothing on the path, nothing found) and is omitted.
//
// This does NOT replicate Postgres' real overload resolution — matching
// candidates across every search_path schema by argument-type compatibility
// — only "which SCHEMA wins," by its own position in searchPath, ignoring
// argument compatibility entirely. A same-named function in an
// earlier-searched schema whose arguments don't actually fit a given call
// site still "wins" here and shadows a later, better-fitting schema :
// accepted as a best-effort heuristic, not a validator, the same stance
// specs/typescript.md already takes for write-shape derivation.
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

// renderFunctionsByName is the bare-name half of specs/typescript.md's
// computed-column story : shapes.ts's ShapeFromCallTag consults this
// UNCONDITIONALLY (unlike ComputedProperties, purely a discoverability
// aid), so `["call", "func_name", ...]` (the unqualified form — the whole
// point of Postgres' `t.func_name()` computed-column sugar) type-resolves
// the same way `["call", {schema,name}, ...]` already does through
// Functions. Always emitted, even empty (Wellknowns' own pattern) : a
// downstream reference to a type that doesn't exist in the file at all is a
// hard compile error, not a graceful "no matches." Each entry is a plain
// indexed reference into Functions, not a re-rendered copy, so an
// overloaded name's union (renderFunctions' own grouping) carries through
// automatically.
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

// firstInputArg returns f's first IN/INOUT/VARIADIC argument — the one a
// Postgres computed-column call (`t.func()`) always binds the row to,
// regardless of how many further arguments f declares.
func firstInputArg(f *pg.Function) *pg.FunctionArgument {
	for i := range f.Arguments {
		a := &f.Arguments[i]
		if a.IsIn() || a.IsInOut() || a.IsVariadic() {
			return a
		}
	}
	return nil
}

// renderComputedProperties is specs/typescript.md's discoverability half of
// computed columns — query-engine.md ## Reading Algorithm's own definition :
// "a function taking the relation's row type as its argument, callable via
// alias.func_name or func_name(alias)". Kept alongside, never merged into,
// Table__/View__ (own/full deliberately never include a computed column) ;
// purely a "what's callable here, and what does it return" discovery aid
// for authoring `select` — the ACTUAL type of a `["call", ...]` expression
// still resolves through FunctionsByName/Functions (shapes.ts's
// ShapeFromCallTag), independently. Deliberately redundant with
// FunctionsByName rather than derived from it at the type level : the two
// answer different questions ("what can I call here" vs "what does this
// specific call produce").
//
// Deliberately NOT filtered through bareNameWinners/search_path, unlike
// FunctionsByName : this session's own testing against a real deployment
// found search_path frequently doesn't cover the schema a project's own
// tables/functions live in (rel's own convention, everywhere else, is to
// never rely on it — relation()/func() always take a fully-qualified
// "schema.relation" name) — gating discoverability on a search_path race
// made it silently vanish even though the QUALIFIED call form
// (["call", {schema,name}, ...], resolved through Functions directly)
// always works regardless of search_path. Eligibility is structural :
// first argument must be this relation's own composite row type, the
// function must live in the SAME schema as the relation, AND be callable
// with exactly that one argument (pg.Function.AcceptsArity(1) — every
// argument after the first has a default) ; a function needing further
// required arguments isn't callable as a bare property. The same-schema
// restriction rules out any naming collision by construction — Postgres
// itself refuses to register two functions in one schema sharing both a
// name AND an identical first-argument type — rather than presenting a
// misleading union of two functions that only coincidentally share a bare
// name (a real risk once cross-schema functions were allowed in : the
// union would suggest "one property, either shape," when an actual bare
// call only ever reaches whichever schema wins FunctionsByName's own
// search_path race, a third, possibly different answer). A cross-schema
// computed function remains fully usable — just not advertised here — via
// the explicit qualified `["call", {schema,name}, ...]` form.
func renderComputedProperties(fns []*pg.Function, allowed map[*pg.Relation]bool, tc *typeCollector) string {
	byRelation := map[*pg.Relation]map[string]string{}
	for _, f := range fns {
		first := firstInputArg(f)
		if first == nil || !f.AcceptsArity(1) {
			continue
		}
		rel := first.Type.CompositeRelation()
		if rel == nil || !allowed[rel] || rel.Identifier.Schema != f.Identifier.Schema {
			continue
		}
		name := f.Identifier.Name
		ts := tsTypeExpr(f.ReturnType, tc)
		if f.ReturnsSet {
			ts += "[]"
		}
		if byRelation[rel] == nil {
			byRelation[rel] = map[string]string{}
		}
		// Never a collision : the same-schema restriction above already
		// rules out two functions sharing both this name and this exact
		// first-argument type (Postgres itself would refuse to register
		// the second one).
		byRelation[rel][name] = ts
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

// renderFunctions is specs/typescript.md ## Schema interfaces' "Functions"
// paragraph. Always emits the interface, even empty (a real deployment
// exporting zero functions) : shapes.ts references `Functions`
// unconditionally (ResolveFunctionModel, FunctionsByName's own indexed
// entries, ShapeFromCallTag), and a downstream reference to a type absent
// from the file entirely is a hard compile error, not a graceful "no
// matches" — the same reasoning Wellknowns/FunctionsByName already follow.
func renderFunctions(fns []*pg.Function, allowed map[*pg.Relation]bool, tc *typeCollector) string {
	// Postgres allows several functions to share one name, distinguished
	// only by argument list (overloading) — grouped by key here so each
	// gets its own union variant instead of colliding on one object-literal
	// key (TS2300 "Duplicate identifier").
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

// functionShapeLiteral renders one function's { positional_args; args;
// relation?; returns } object literal — shared by the single-overload and
// unioned-overloads cases in renderFunctions above.
func functionShapeLiteral(f *pg.Function, allowed map[*pg.Relation]bool, tc *typeCollector) string {
	var b strings.Builder
	b.WriteString("{\n")
	renderFunctionArgs(&b, f, tc)
	renderFunctionReturns(&b, f, allowed, tc)
	b.WriteString("  }")
	return b.String()
}

// inputArg is one callable (IN/INOUT/VARIADIC) argument, in declaration
// order — the same set positional_args/args both describe.
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
	// PgNargsDefaults counts trailing input args with a default —
	// info_function.go's own doc comment on Function.PgNargsDefaults.
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
	argsType := "EmptyObject" // a zero-argument function's own args object — see EmptyObject's own doc comment
	if len(obj) > 0 {
		argsType = "{ " + strings.Join(obj, "; ") + " }"
	}
	fmt.Fprintf(b, "    args: %s\n", argsType)
}

// renderFunctionReturns is specs/typescript.md ## Schema interfaces'
// "Functions" paragraph's `relation`/`returns` half : `relation` present
// only for a set-returning function whose row shape matches a real target
// Table__/View__ ; a RETURNS TABLE/OUT-parameter function with no matching
// real relation falls back to an inline object shape instead.
func renderFunctionReturns(b *strings.Builder, f *pg.Function, allowed map[*pg.Relation]bool, tc *typeCollector) {
	if f.ReturnsSet {
		if rel := f.ReturnType.CompositeRelation(); rel != nil && allowed[rel] {
			name := relationInterfaceName(rel.Identifier.Schema, rel.Identifier.Name, rel.IsView)
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

// recordRelationTypeExpr renders a function's own synthetic RecordRelation
// (RETURNS TABLE/OUT-parameter columns) as an inline object type. info_type.
// go only ever builds a RecordRelation when it has at least one column, so
// this can't actually be empty today — guarded anyway, defensively, the
// same as renderRelationInterfaces' own zero-column case, rather than
// relying on that upstream invariant never changing.
func recordRelationTypeExpr(r *pg.Relation, tc *typeCollector) string {
	body := relationColumnsBlock(r, tc)
	if body == "" {
		return "EmptyObject"
	}
	return "{\n" + indentBlock(body, "    ") + "    }"
}

// indentBlock prepends prefix to every line of s (relationColumnsBlock's own
// two-space indent is meant for a top-level interface body, one level
// shallower than an inline `returns:` object needs).
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
