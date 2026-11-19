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
	return nodeCBOR(root, 0)
}

const cborReachMaxDepth = 64

func nodeCBOR(node *QueryNode, depth int) string {
	if depth > cborReachMaxDepth {
		return ""
	}
	if node.IsFunction() && node.Relation == nil && containsCBOR(node.Function.ReturnType) {
		return "the result of " + node.Function.Identifier.String()
	}
	if !isShapeProducingSelect(node.Select) {
		return exprCBOR(node.Select, depth)
	}
	fields, _ := selectFieldsFor(node)
	for _, f := range fields {
		switch {
		case f.column != nil:
			if containsCBOR(f.column.Type) {
				return columnName(node, f.column)
			}
		case embedChildOf(f) != nil:
			if what := nodeCBOR(embedChildOf(f), depth+1); what != "" {
				return what
			}
		default:
			if what := exprCBOR(f.expr, depth); what != "" {
				return what
			}
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

// exprCBOR is nodeCBOR for one output expression : only the ways a value passes into the result are followed, not
// the operands of a comparison or of a function with a known return type.
func exprCBOR(e Expression, depth int) string {
	if depth > cborReachMaxDepth {
		return ""
	}
	depth++
	switch v := e.(type) {
	case *Identifier:
		return resolvedCBOR(v.Resolved, depth)
	case *ColExpr:
		return resolvedCBOR(v.Resolved, depth)
	case *GetExpr:
		if v.ResolvedColumn != nil && containsCBOR(v.ResolvedColumn.Type) {
			return "column " + v.ResolvedColumn.Name
		}
	case FoldedExpr:
		switch v.Op {
		case FoldDot:
			return exprCBOR(v.Right, depth)
		case FoldJsonGet, FoldJsonGetText, FoldJsonPathGet, FoldJsonPathGetText:
			return exprCBOR(v.Left, depth)
		}
		if what := exprCBOR(v.Left, depth); what != "" {
			return what
		}
		return exprCBOR(v.Right, depth)
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
		return functionCBOR(v.ResolvedFunction, v.Arguments, depth)
	case *AggExpr:
		return functionCBOR(v.ResolvedFunction, v.Arguments, depth)
	case CoalesceExpr:
		return firstCBOR(v.Args, depth)
	case ArrExpr:
		return firstCBOR(v.Items, depth)
	case LstExpr:
		return firstCBOR(v.Items, depth)
	case IndexExpr:
		return exprCBOR(v.Array, depth)
	case SliceExpr:
		return exprCBOR(v.Array, depth)
	case ObjectExpr:
		for _, f := range v.Fields {
			if what := exprCBOR(f, depth); what != "" {
				return what
			}
		}
	}
	return ""
}

func firstCBOR(exprs []Expression, depth int) string {
	for _, a := range exprs {
		if what := exprCBOR(a, depth); what != "" {
			return what
		}
	}
	return ""
}

// functionCBOR checks a function's return type ; a polymorphic one returns what its arguments are.
func functionCBOR(f *pg.Function, args []Expression, depth int) string {
	if f == nil || f.ReturnType == nil {
		return ""
	}
	if isPolymorphic(f.ReturnType) {
		return firstCBOR(args, depth)
	}
	if containsCBOR(f.ReturnType) {
		return "the result of " + f.Identifier.String()
	}
	return ""
}

func resolvedCBOR(r ResolvedField, depth int) string {
	switch v := r.(type) {
	case ColumnPath:
		if containsCBOR(v.CurrentType()) {
			return columnName(v.Node, v.Path[len(v.Path)-1])
		}
	case *QueryNode:
		return nodeCBOR(v, depth)
	case ComputedFieldRef:
		if containsCBOR(v.Function.ReturnType) {
			return "computed field " + v.Function.Identifier.Name
		}
	}
	return ""
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

// rootBytesAreCBOR reports whether the scalar values root's statement yields are a bytea-domain CBOR column's :
// Postgres sends them with bytea's oid, a domain's own being kept only inside records and arrays.
func rootBytesAreCBOR(root *QueryNode) bool {
	if root.IsFunction() && root.Relation == nil {
		return isCBOR(root.Function.ReturnType)
	}
	if isShapeProducingSelect(root.Select) {
		return false
	}
	e := root.Select
	if f, ok := e.(FoldedExpr); ok && f.Op == FoldDot {
		e = f.Right
	}
	if c, ok := e.(*CallExpr); ok {
		return c.ResolvedFunction != nil && isCBOR(c.ResolvedFunction.ReturnType)
	}
	switch r := bareResolvedField(e).(type) {
	case ColumnPath:
		return isCBOR(r.CurrentType())
	case ComputedFieldRef:
		return isCBOR(r.Function.ReturnType)
	}
	return false
}

func isCBOR(t *pg.Type) bool { return t != nil && t.CBOR }
