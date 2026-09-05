package query

import (
	"fmt"
	"strings"

	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/writer"
)

// ColumnPath is the column-backed ResolvedField variant : a plain column of
// Node's own relation (len(Path)==1) or a composite sub-field reached by
// walking successive *pg.Type.Relation.ColumnsMap off it (len(Path)>1).
//
// Node+Path together are the identity key for occurrence-counting/
// extraction, not the terminal *pg.Column alone : two columns sharing the
// same composite type (e.g. two addr_t-typed columns) yield the *same*
// *pg.Column pointer once you navigate into it, so Path[0]'s identity (or
// Node's) has to be part of the key or "home.city" and "work.city" would
// collide. Composite sub-fields are independently writable (decided this
// session), so the *full* path matters, not just the containing column.
type ColumnPath struct {
	Node *QueryNode
	Path []*pg.Column

	// ElementType overrides CurrentType()'s result when set — used after an
	// ["index", ...] hop unwraps one level of array : further "." navigation
	// must check the array's ELEMENT type, not Path's last column's own
	// declared (array) type. nil in the ordinary (non-indexed) case.
	ElementType *pg.Type
}

// CurrentType is the type further "." navigation from this path should
// check for compositeness — Path's last column's own type, unless
// ElementType overrides it (after an array-index hop).
func (c ColumnPath) CurrentType() *pg.Type {
	if c.ElementType != nil {
		return c.ElementType
	}
	if len(c.Path) == 0 {
		return nil
	}
	return c.Path[len(c.Path)-1].Type
}

// Key returns a canonical, comparable string identifying this path — Go map
// keys must be comparable and []*pg.Column isn't, so occurrence-counting and
// extraction key on this instead of the struct itself.
func (c ColumnPath) Key() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%p", c.Node)
	for _, col := range c.Path {
		b.WriteByte('.')
		b.WriteString(col.Name)
	}
	return b.String()
}

// WriteQualifiedPath emits path (a ColumnPath's own Path, or a prefix of
// one) rooted at alias : "alias.a" for a single segment, Postgres's
// row-value parenthesization for further ones — "(alias.a).b",
// "((alias.a).b).c" — since "alias.a.b" is not legal syntax for "column a,
// then sub-field b," it must be parenthesized one level at a time. Shared
// between sql_expr.go's own read-path compilation (a "." chain landing on
// a same-node ColumnPath) and write_dml.go's "excluded.col" references in
// an UPSERT's ON CONFLICT DO UPDATE SET — the one other place composite
// sub-field navigation off an aliased ROW value (not a flat CTE column) is
// needed.
func WriteQualifiedPath(w *writer.SQLWriter, alias string, path []*pg.Column) {
	writeQualifiedPathN(w, alias, path, len(path)-1)
}

func writeQualifiedPathN(w *writer.SQLWriter, alias string, path []*pg.Column, i int) {
	if i == 0 {
		w.Write(alias)
		w.Write(".")
		w.Id(path[0].Name)
		return
	}
	w.Paren(func() {
		writeQualifiedPathN(w, alias, path, i-1)
	})
	w.Write(".")
	w.Id(path[i].Name)
}

// columnPathFlatName is a ColumnPath's synthetic flat name : Path[0].Name,
// or "__"-joined segments for a composite sub-field (e.g. "home__city").
func columnPathFlatName(cp ColumnPath) string {
	if len(cp.Path) == 1 {
		return cp.Path[0].Name
	}
	names := make([]string, len(cp.Path))
	for i, c := range cp.Path {
		names[i] = c.Name
	}
	return strings.Join(names, "__")
}

// ComputedFieldRef is the computed-field-backed ResolvedField variant — a
// function eligible on Node's own relation (pg.Relation.ComputedFields),
// reached by its bare name exactly like a real column. Unlike ColumnPath, a
// ComputedFieldRef is opaque : never a write target (it isn't a real column
// to begin with), never chainable further via "." (same as any other
// function call's result), and never itself select/join-customizable even
// when Function's return type matches another relation or SETOF one — a
// relation-shaped computed field always compiles as the bare full row(s)
// value, exactly like ["call", fn, alias] does today. Compiled by
// compileComputedFieldRef (sql_expr.go).
type ComputedFieldRef struct {
	Node     *QueryNode
	Function *pg.Function
}

// Shape is the "produces a map of named fields" ResolvedField variant — the
// landing of ANY select-shape-producing expression, uniformly : own/full
// (and their -except/-and variants), an inline object literal, or one
// nested inside another of these. There's no distinction here between "the
// top-level select of a node" and "a shape-producing expression appearing
// anywhere else in the tree" — own_and nested three levels inside an object
// literal builds and is chained into exactly the same way own_and used as a
// node's whole select is. Built eagerly (every key's own landing resolved
// as part of producing the Shape, via resolveChain — see buildShape in
// expression_resolve.go), so a later hop into it is just a map lookup, no
// further resolution needed at that point.
type Shape map[string]ResolvedField

// ResolvedField is what resolving an Identifier (or a later hop in a
// ./->/->>/#>/#>> chain) produces — see specs/query-engine.md's
// "## Scoping ### Identifier resolution" section for the full reasoning. Four
// concrete variants :
//   - ColumnPath : a physical column, or a composite sub-field reached by
//     walking *pg.Type.Relation.ColumnsMap off one. Already fully
//     introspected by pg ; no new DB-side work needed for the composite
//     case. A leaf in "." chain terms unless the terminal column is itself
//     composite.
//   - *QueryNode : an embedded join alias (OuterAlias), or a self-reference
//     (a node's own InnerName resolving to itself). A further hop resolves
//     against its full Scope (LookupInScope) — this is "into a child from
//     itself," not "into a sibling," so it doesn't violate the
//     no-sibling-access rule.
//   - Shape : a key into a select-shape-producing expression, as above.
//   - ComputedFieldRef : a computed field (pg.Relation.ComputedFields),
//     resolved by bare name exactly like a physical column, but opaque like
//     a plain function call — see ComputedFieldRef's own doc comment.
//
// nil means "opaque" — the landing spot of a ->/->>/#>/#>> hop (jsonb
// field/path extraction, or composite-row dot access via those operators is
// not how "." itself works here), or of any expression that isn't one of
// the four shapes above (arithmetic, an explicit ["call", ...], ...). An
// opaque landing contributes nothing to shape/writability and can only be
// chained further via more of the same JSON operators, never a "." hop.
//
// No cycle detection needed to resolve any of this : with sibling visibility
// excluded, a "." chain only ever travels downward into a node's own
// already-parsed children — a finite JSON-derived tree, not a graph.
// Memoization is worth keeping (a given node's fields can legitimately be
// requested from more than one place) but only as a performance nicety, not
// for correctness.
//
// Scoping/blacklist checks apply uniformly to every hop. A composite column
// is walkable the moment the query legitimately has access to the column it
// lives on — there is no separate gate for composite sub-fields, and
// reaching an otherwise-blacklisted relation this way is allowed by design :
// the developer may want to expose it in a controlled way precisely by
// routing access through a specific column.
type ResolvedField any
