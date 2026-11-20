package tsgen

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/declcomment"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/query"
)

// Declared types : a `column::`, `table::` or `type::` comment, or a typescript.types entry, which wins over a
// column's comment. Generation applies what is valid and silently skips the rest ; DeclarationWarnings says why.

// declSource resolves declarations against the configured typescript.types.
type declSource struct{ config map[string]config.TypeDecl }

// column is c's declaration, and the warnings it raises ; r is the relation or composite type c belongs to.
func (s declSource) column(r *pg.Relation, c *pg.Column) (declcomment.TypeDecl, []string) {
	where := "column " + relationKey(r.Identifier.Schema, r.Identifier.Name) + "." + c.Name
	d, warnings := parseDecl(c.Comment, "column", where)
	if entry, ok := s.config[relationKey(r.Identifier.Schema, r.Identifier.Name)+"."+c.Name]; ok {
		cd := declcomment.TypeDecl{Comment: entry.Comment, Type: entry.Type, From: entry.From}
		if err := cd.Validate(); err != nil {
			warnings = append(warnings, fmt.Sprintf("typescript.types for %s ignored : %v", where, err))
		} else {
			if d.Type != "" && cd.Type != "" {
				warnings = append(warnings, fmt.Sprintf("typescript.types overrides the type declared in the comment on %s", where))
			}
			if cd.Type != "" {
				d.Type, d.From = cd.Type, cd.From
			}
			if cd.Comment != "" {
				d.Comment = cd.Comment
			}
		}
	}
	if d.Type != "" && !declarable(c.Type) {
		warnings = append(warnings, fmt.Sprintf("%s : a declared type only applies to a json, jsonb or cbor column, not %s ; ignored", where, c.Type.PgIdentifier.String()))
		d.Type, d.From = "", ""
	}
	return d, warnings
}

// relation is r's doc text : a `table::` comment has only a comment.
func (s declSource) relation(r *pg.Relation) (string, []string) {
	where := "relation " + relationKey(r.Identifier.Schema, r.Identifier.Name)
	d, warnings := parseDecl(r.Comment, "table", where)
	if d.Type != "" || d.From != "" {
		warnings = append(warnings, where+" : a table:: declaration has only a comment ; type and from ignored")
	}
	return d.Comment, warnings
}

// typ is t's declaration : only a domain over json, jsonb or cbor takes a type.
func (s declSource) typ(t *pg.Type) (declcomment.TypeDecl, []string) {
	where := "type " + t.PgIdentifier.String()
	d, warnings := parseDecl(t.Comment, "type", where)
	if d.Type != "" && (!t.IsDomain() || !declarable(t)) {
		warnings = append(warnings, where+" : a declared type only applies to a domain over json, jsonb or cbor ; ignored")
		d.Type, d.From = "", ""
	}
	return d, warnings
}

// parseDecl reads comment as a kind declaration ; a plain comment is its own doc text, a broken declaration none.
func parseDecl(comment, kind, where string) (declcomment.TypeDecl, []string) {
	raw, found, err := declcomment.Extract(comment, kind)
	if !found {
		return declcomment.TypeDecl{Comment: comment}, nil
	}
	var d declcomment.TypeDecl
	if err == nil {
		d, err = declcomment.Decode(raw)
	}
	if err != nil {
		return declcomment.TypeDecl{}, []string{fmt.Sprintf("the %s:: comment on %s is ignored : %v", kind, where, err)}
	}
	return d, nil
}

// declarable reports a json, jsonb or CBOR type, or an array or domain of one.
func declarable(t *pg.Type) bool {
	for depth := 0; t != nil && depth < 16; depth++ {
		if t.CBOR {
			return true
		}
		switch {
		case t.IsArray():
			t = t.ElementType
		case t.IsDomain():
			t = t.BaseType
		default:
			return t.PgIdentifier.Schema == "pg_catalog" && (t.PgIdentifier.Name == "json" || t.PgIdentifier.Name == "jsonb")
		}
	}
	return false
}

// DeclarationWarnings lists what is wrong with the declarations generation would read under opts : a broken
// comment, a type on a column that cannot take one, a typescript.types entry overriding a comment or naming no
// column. Generation itself is silent, since it runs per request.
func DeclarationWarnings(db *pg.DbInfos, opts Options) []string {
	src := declSource{config: opts.Types}
	var out []string
	known := map[string]bool{}
	for _, r := range db.Relations {
		for _, c := range r.Columns {
			known[relationKey(r.Identifier.Schema, r.Identifier.Name)+"."+c.Name] = true
		}
		if r.IsSynthetic || !opts.schemaAllowed(r.Identifier.Schema) {
			continue
		}
		if !r.IsStandaloneCompositeType {
			_, w := src.relation(r)
			out = append(out, w...)
		}
		for _, c := range r.Columns {
			_, w := src.column(r, c)
			out = append(out, w...)
		}
	}
	for _, t := range db.TypeMapByOid {
		if (t.IsDomain() || t.IsEnum() || t.IsComposite()) && opts.schemaAllowed(t.PgIdentifier.Schema) {
			_, w := src.typ(t)
			out = append(out, w...)
		}
	}
	for key := range opts.Types {
		if !known[key] {
			out = append(out, fmt.Sprintf("typescript.types.%s names no column", key))
		}
	}
	sort.Strings(out)
	return out
}

// importSet collects the names declarations import ; each use is a placeholder until the file is whole, since an
// imported name must not collide with any name the file holds.
type importSet struct {
	index map[[2]string]int // [from, name]
	refs  [][2]string
}

func (s *importSet) ref(from, name string) string {
	key := [2]string{from, name}
	i, ok := s.index[key]
	if !ok {
		if s.index == nil {
			s.index = map[[2]string]int{}
		}
		i = len(s.refs)
		s.index[key] = i
		s.refs = append(s.refs, key)
	}
	return importPlaceholder(i)
}

func importPlaceholder(i int) string { return fmt.Sprintf("__rel_import_%d__", i) }

var identifierToken = regexp.MustCompile(`[A-Za-z_$][\w$]*`)

// commentOrString is a comment or a one-line string literal, whose words name nothing ; template literals are
// kept, being over-cautious costs only an alias.
var commentOrString = regexp.MustCompile(`//[^\n]*|/\*[\s\S]*?\*/|"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'`)

// resolve replaces each placeholder in body by its local name, and returns the import statements to put before it.
// A name keeps its own spelling unless body already uses it as any identifier (over-cautious and harmless), or a
// name from another module took it ; then it is aliased Name__2, Name__3...
func (s *importSet) resolve(body string) (imports, out string) {
	if len(s.refs) == 0 {
		return "", body
	}
	taken := map[string]bool{}
	for _, tok := range identifierToken.FindAllString(commentOrString.ReplaceAllString(body, " "), -1) {
		taken[tok] = true
	}
	order := make([]int, len(s.refs))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		ra, rb := s.refs[order[a]], s.refs[order[b]]
		if ra[0] != rb[0] {
			return ra[0] < rb[0]
		}
		return ra[1] < rb[1]
	})
	local := make([]string, len(s.refs))
	byModule := map[string][]string{}
	var modules []string
	replacements := make([]string, 0, 2*len(s.refs))
	for _, i := range order {
		from, name := s.refs[i][0], s.refs[i][1]
		alias := name
		for n := 2; taken[alias]; n++ {
			alias = fmt.Sprintf("%s__%d", name, n)
		}
		taken[alias] = true
		local[i] = alias
		spec := name
		if alias != name {
			spec = name + " as " + alias
		}
		if _, ok := byModule[from]; !ok {
			modules = append(modules, from)
		}
		byModule[from] = append(byModule[from], spec)
		replacements = append(replacements, importPlaceholder(i), alias)
	}
	var b strings.Builder
	for _, m := range modules {
		fmt.Fprintf(&b, "import type { %s } from %q\n", strings.Join(byModule[m], ", "), m)
	}
	return b.String(), strings.NewReplacer(replacements...).Replace(body)
}

// declaredTypeExpr is d's type as the generated file spells it : an import placeholder for a name from a module.
func declaredTypeExpr(d declcomment.TypeDecl, tc *typeCollector) string {
	if d.From != "" {
		return tc.imports.ref(d.From, d.Type)
	}
	return d.Type
}

// cborUnreadable reports a value the client's wire format cannot carry : under JSON, the server refuses a CBOR
// column's value either way, so it is typed never.
func (tc *typeCollector) cborUnreadable(t *pg.Type) bool {
	return tc.jsonWire && query.ContainsCBOR(t)
}
