package query

import (
	"fmt"
	"strings"

	"github.com/ceymard/rel/pg"
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

// LiteralField is the inline-object-literal-backed ResolvedField variant :
// Node is the QueryNode whose select this literal lives in — its Fields
// values are themselves unresolved Expression trees, resolved against
// Node's scope on demand as each key is actually chained into, not
// pre-resolved when the literal itself is first reached.
type LiteralField struct {
	Node   *QueryNode
	Fields map[string]Expression
}

// ResolvedField is what resolving an Identifier (or a later hop in a
// ./->/->>/#>/#>> chain) produces — see specs/query-compiler.md's
// "Identifier resolution" section for the full reasoning. Three concrete
// variants :
//   - ColumnPath   : a physical column, or a composite sub-field reached by
//     walking *pg.Type.Relation.ColumnsMap off one. Already fully
//     introspected by pg ; no new DB-side work needed for the composite
//     case. A leaf in "." chain terms unless the terminal column is itself
//     composite.
//   - *QueryNode   : an embedded join alias (OuterAlias), or a
//     self-reference (a node's own InnerName resolving to itself). A
//     further hop resolves against its full Scope (LookupInScope) — this is
//     "into a child from itself," not "into a sibling," so it doesn't
//     violate the no-sibling-access rule.
//   - LiteralField : a key into a nested inline object literal from the
//     select JSON ; purely syntactic, no DB lookup, each key recurses into
//     whichever of the above it turns out to be once resolved.
//
// nil means "opaque" — the landing spot of a ->/->>/#>/#>> hop (jsonb
// field/path extraction, or composite-row dot access via those operators is
// not how "." itself works here). An opaque landing contributes nothing to
// shape/writability and can only be chained further via more of the same
// JSON operators, never a "." hop.
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
