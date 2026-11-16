package query

import (
	"fmt"
	"strings"

	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/writer"
)

// ColumnPath is a plain column of Node's relation (one-element Path) or a composite sub-field under it. Node and
// the full Path identify it, not the last column : two columns of one composite type share their sub-field
// columns, so "home.city" and "work.city" would collide. Composite sub-fields are writable on their own.
type ColumnPath struct {
	Node *QueryNode
	Path []*pg.Column

	// ElementType, set after an ["index", ...] hop, is the array's element type, which further "." hops use.
	ElementType *pg.Type
}

// CurrentType is the type further "." hops navigate : ElementType if set, else the last column's type.
func (c ColumnPath) CurrentType() *pg.Type {
	if c.ElementType != nil {
		return c.ElementType
	}
	if len(c.Path) == 0 {
		return nil
	}
	return c.Path[len(c.Path)-1].Type
}

// Key returns a comparable string identifying this path, for use as a map key.
func (c ColumnPath) Key() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%p", c.Node)
	for _, col := range c.Path {
		b.WriteByte('.')
		b.WriteString(col.Name)
	}
	return b.String()
}

// WriteQualifiedPath writes path rooted at alias : "alias.a", then "(alias.a).b", "((alias.a).b).c".
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

// ComputedFieldRef is a computed field of Node's relation, named like a column. It is opaque : never written,
// never chained with ".", and a relation-returning one always compiles to its full row(s), like ["call", fn, alias].
type ComputedFieldRef struct {
	Node     *QueryNode
	Function *pg.Function
}

// Shape is the landing of any shape-producing expression ("*", "*~", an object), whether a node's whole select or
// nested anywhere. Every key is resolved when it is built, so a hop into it is a map lookup.
type Shape map[string]ResolvedField

// ResolvedField is what resolving an Identifier or a later chain hop produces :
//   - ColumnPath : a column or composite sub-field ; a leaf unless the last column is composite.
//   - *QueryNode : a join alias (OuterAlias) or the node's own InnerName ; a further hop resolves in its Scope.
//   - Shape : a key into a shape-producing expression.
//   - ComputedFieldRef : a computed field.
//
// nil is opaque : a ->/->>/#>/#>> landing or any other expression. It adds nothing to shape/writability and only
// chains further through those JSON operators, never ".".
//
// A "." chain only goes down into a node's own children (siblings are invisible), so no cycle detection is needed.
// Reaching a blacklisted relation through a composite column the query may access is allowed by design, so it can
// be exposed in a controlled way.
type ResolvedField any
