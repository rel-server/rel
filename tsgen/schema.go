package tsgen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/pg"
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
// layout's Section 2 : Relations/Relationships/Functions/Wellknowns and the
// Table__/View__/Type__ interfaces they reference, from db filtered by opts.
func GenerateSchema(db *pg.DbInfos, opts Options) string {
	tc := newTypeCollector()

	relations := targetRelations(db, opts)
	relInterfaces := renderRelationInterfaces(relations, tc)
	relationsMap := renderRelationsMap(relations)
	relationships := renderRelationships(relations, opts, tc)
	functions := renderFunctions(db, opts, tc)

	var b strings.Builder
	b.WriteString(tc.declarations())
	b.WriteString(relInterfaces)
	b.WriteString(relationsMap)
	b.WriteString(relationships)
	b.WriteString(functions)
	// specs/typescript.md ## Wellknowns : reserved, generated empty until
	// well-known queries are introspectable server-side.
	b.WriteString("// Well-known queries aren't introspectable yet.\n")
	b.WriteString("// biome-ignore lint/suspicious/noEmptyInterface: reserved, see specs/typescript.md ## Wellknowns\n")
	b.WriteString("export interface Wellknowns {}\n")
	return b.String()
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

func renderRelationInterfaces(relations []*pg.Relation, tc *typeCollector) string {
	var b strings.Builder
	for _, r := range relations {
		name := relationInterfaceName(r.Identifier.Schema, r.Identifier.Name, r.IsView)
		b.WriteString(docComment(r.Comment, ""))
		fmt.Fprintf(&b, "interface %s {\n%s}\n\n", name, relationColumnsBlock(r, tc))
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
func renderRelationships(relations []*pg.Relation, opts Options, tc *typeCollector) string {
	allowed := map[*pg.Relation]bool{}
	for _, r := range relations {
		allowed[r] = true
	}

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

// renderFunctions is specs/typescript.md ## Schema interfaces' "Functions"
// paragraph.
func renderFunctions(db *pg.DbInfos, opts Options, tc *typeCollector) string {
	allowed := map[*pg.Relation]bool{}
	for _, r := range targetRelations(db, opts) {
		allowed[r] = true
	}

	var fns []*pg.Function
	for _, f := range db.Functions {
		if opts.targetFunction(f) {
			fns = append(fns, f)
		}
	}
	if len(fns) == 0 {
		return ""
	}
	sort.Slice(fns, func(i, j int) bool {
		if fns[i].Identifier.Schema != fns[j].Identifier.Schema {
			return fns[i].Identifier.Schema < fns[j].Identifier.Schema
		}
		return fns[i].Identifier.Name < fns[j].Identifier.Name
	})

	var b strings.Builder
	b.WriteString("export interface Functions {\n")
	for _, f := range fns {
		key := relationKey(f.Identifier.Schema, f.Identifier.Name)
		fmt.Fprintf(&b, "  %s: {\n", strconv.Quote(key))
		renderFunctionArgs(&b, f, tc)
		renderFunctionReturns(&b, f, allowed, tc)
		b.WriteString("  }\n")
	}
	b.WriteString("}\n\n")
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
	fmt.Fprintf(b, "    args: { %s }\n", strings.Join(obj, "; "))
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
			fmt.Fprintf(b, "    returns: {\n%s    }[]\n", indentBlock(relationColumnsBlock(f.RecordRelation, tc), "    "))
			return
		}
		fmt.Fprintf(b, "    returns: %s[]\n", tsTypeExpr(f.ReturnType, tc))
		return
	}
	if f.RecordRelation != nil {
		fmt.Fprintf(b, "    returns: {\n%s    }\n", indentBlock(relationColumnsBlock(f.RecordRelation, tc), "    "))
		return
	}
	fmt.Fprintf(b, "    returns: %s\n", tsTypeExpr(f.ReturnType, tc))
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
