package query

import (
	"strings"

	"github.com/rel-server/rel/pg"
)

// A JSON response only carries what JSON can say : a query whose output holds a CBOR column's value is refused
// before it runs. Filters and sort keys are not output and are not looked at.

// CBORReached names the first CBOR value root's output holds ("column data of public.doc"), or "" when there is
// none. It reads a resolved tree and does not modify it.
func CBORReached(root *QueryNode) string {
	var w cborWalk
	return w.node(root)
}

// cborWalk visits each node once : a query may name the same child under many keys.
type cborWalk struct{ seen map[*QueryNode]bool }

func (w *cborWalk) node(node *QueryNode) string {
	if w.seen[node] {
		return ""
	}
	if w.seen == nil {
		w.seen = map[*QueryNode]bool{}
	}
	w.seen[node] = true
	if node.IsFunction() && node.Relation == nil && containsCBOR(node.Function.ReturnType) {
		return "the result of " + node.Function.Identifier.String()
	}
	if !isShapeProducingSelect(node.Select) {
		return w.expr(node, node.Select)
	}
	fields, _ := selectFieldsFor(node)
	for _, f := range fields {
		var what string
		switch child := embedChildOf(f); {
		case f.column != nil:
			if containsCBOR(f.column.Type) {
				what = columnName(node, f.column)
			}
		case child != nil:
			what = w.node(child)
		default:
			what = w.expr(node, f.expr)
		}
		if what != "" {
			return what
		}
	}
	return ""
}

// expr is node for one output expression of node : only the ways a value passes into the result are followed, not
// the operands of a comparison or of a function with a known return type.
func (w *cborWalk) expr(node *QueryNode, e Expression) string {
	switch v := e.(type) {
	case *Identifier:
		return resolvedCBOR(v.Resolved)
	case *ColExpr:
		return resolvedCBOR(v.Resolved)
	case *GetExpr:
		if v.ResolvedColumn != nil && containsCBOR(v.ResolvedColumn.Type) {
			return columnName(node, v.ResolvedColumn)
		}
	case FoldedExpr:
		switch v.Op {
		case FoldDot:
			return w.expr(node, v.Right)
		case FoldJsonGet, FoldJsonGetText, FoldJsonPathGet, FoldJsonPathGetText:
			return w.expr(node, v.Left)
		}
		return w.first(node, v.Left, v.Right)
	case BinaryExpr:
		if v.Op == BinaryCast {
			if s, ok := v.Right.(StringLiteral); ok && isCBORTypeName(s.Value) {
				return "a cast to " + s.Value
			}
		}
	case ParamExpr:
		if isCBORTypeName(v.Cast) {
			return "parameter " + v.Name
		}
	case *CallExpr:
		return w.function(node, v.ResolvedFunction, v.Arguments)
	case *AggExpr:
		return w.function(node, v.ResolvedFunction, v.Arguments)
	case CoalesceExpr:
		return w.first(node, v.Args...)
	case ArrExpr:
		return w.first(node, v.Items...)
	case LstExpr:
		return w.first(node, v.Items...)
	case IndexExpr:
		return w.expr(node, v.Array)
	case SliceExpr:
		return w.expr(node, v.Array)
	case ObjectExpr:
		for _, f := range v.Fields {
			if what := w.expr(node, f); what != "" {
				return what
			}
		}
	case StarExpr:
		// node's own columns, and for "*" its children, as the node's own select would hold them
		excluded := map[string]bool{}
		for _, name := range v.Except {
			excluded[name] = true
		}
		if node.Relation != nil {
			for _, c := range node.Relation.Columns {
				if !excluded[c.Name] && containsCBOR(c.Type) {
					return columnName(node, c)
				}
			}
		}
		if !v.Own {
			for _, c := range append(append([]*QueryNode{}, node.OutgoingNodes...), node.IncomingNodes...) {
				if what := w.node(c); what != "" {
					return what
				}
			}
		}
		for _, f := range v.And {
			if what := w.expr(node, f); what != "" {
				return what
			}
		}
	}
	return ""
}

func (w *cborWalk) first(node *QueryNode, exprs ...Expression) string {
	for _, a := range exprs {
		if what := w.expr(node, a); what != "" {
			return what
		}
	}
	return ""
}

// function checks a function's return type ; a polymorphic one returns what its arguments are.
func (w *cborWalk) function(node *QueryNode, f *pg.Function, args []Expression) string {
	if f == nil || f.ReturnType == nil {
		return ""
	}
	if isPolymorphic(f.ReturnType) {
		return w.first(node, args...)
	}
	if containsCBOR(f.ReturnType) {
		return "the result of " + f.Identifier.String()
	}
	return ""
}

// resolvedCBOR checks what a name resolved to. A node named as a value is its relation's whole row.
func resolvedCBOR(r ResolvedField) string {
	switch v := r.(type) {
	case ColumnPath:
		if containsCBOR(v.CurrentType()) {
			return columnName(v.Node, v.Path[len(v.Path)-1])
		}
	case *QueryNode:
		if v.Relation != nil {
			for _, c := range v.Relation.Columns {
				if containsCBOR(c.Type) {
					return columnName(v, c)
				}
			}
		}
	case ComputedFieldRef:
		if containsCBOR(v.Function.ReturnType) {
			return "computed field " + v.Function.Identifier.Name
		}
	}
	return ""
}

func columnName(node *QueryNode, col *pg.Column) string {
	if node != nil && node.Relation != nil {
		return "column " + col.Name + " of " + node.Relation.Identifier.String()
	}
	return "column " + col.Name
}

// containsCBOR reports whether a value of type t is, or holds in an array or composite, a CBOR column's value.
func containsCBOR(t *pg.Type) bool { return containsCBORDepth(t, 0) }

func containsCBORDepth(t *pg.Type, depth int) bool {
	if t == nil || depth > 16 {
		return false
	}
	if t.CBOR {
		return true
	}
	u := t.Underlying()
	switch {
	case u.IsArray():
		return containsCBORDepth(u.ElementType, depth+1)
	case u.IsComposite():
		for _, c := range u.Relation.Columns {
			if containsCBORDepth(c.Type, depth+1) {
				return true
			}
		}
	}
	return false
}

// isPolymorphic reports one of Postgres' polymorphic pseudo-types (anyelement, anyarray, anycompatible...).
func isPolymorphic(t *pg.Type) bool {
	return t.PgIdentifier.Schema == "pg_catalog" && strings.HasPrefix(t.PgIdentifier.Name, "any")
}

// isCBORTypeName reports a type name, as a cast writes it, naming a CBOR column's type : cbor, schema.cbor, or an
// array of it. A domain under another name is only known through a column.
func isCBORTypeName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for strings.HasSuffix(name, "[]") {
		name = strings.TrimSpace(strings.TrimSuffix(name, "[]"))
	}
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return strings.Trim(name, `"`) == "cbor"
}

// CBORWritten names the first CBOR column root's write shape holds, or "" : a JSON request has no CBOR bytes to
// write into one, even when its rows leave the column out.
func CBORWritten(root *QueryNode) string {
	if root.Shape != nil {
		for _, x := range root.Shape.Extractors {
			if containsCBOR(x.Path.CurrentType()) {
				return columnName(x.Path.Node, x.Path.Path[len(x.Path.Path)-1])
			}
		}
	}
	for _, c := range append(append([]*QueryNode{}, root.OutgoingNodes...), root.IncomingNodes...) {
		if what := CBORWritten(c); what != "" {
			return what
		}
	}
	return ""
}

// IsCBORNode reports whether n was read from a CBOR request.
func IsCBORNode(n Node) bool {
	_, ok := n.(cborAppender)
	return ok
}

// rootBytesAreCBOR reports whether the bytea values root's statement yields are a bytea-domain CBOR column's :
// Postgres sends them with bytea's oid, a domain's own being kept only inside records and arrays. A bytea value
// from an expression that holds a CBOR value can only be that value.
func rootBytesAreCBOR(root *QueryNode) bool {
	if root.IsFunction() && root.Relation == nil {
		return root.Function.ReturnType != nil && root.Function.ReturnType.CBOR
	}
	return !isShapeProducingSelect(root.Select) && CBORReached(root) != ""
}
