// Pass 3, expression compilation : turns one already-resolved Expression
// (query/expression.go, resolved by pass 2 — see expression_resolve.go) into
// SQL text via the shared sqlCompiler (sql.go). One case per Expression
// type, mirroring resolveExpr's own switch in expression_resolve.go.
package query

import (
	"fmt"
	"regexp"
	"sort"

	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/writer"
)

// compileExpr writes e's SQL text into c.w. n is the node e was resolved
// against, needed for own/full's implicit column list and self-references.
func (c *sqlCompiler) compileExpr(e Expression, n *QueryNode) error {
	switch v := e.(type) {
	case nil:
		c.w.Write("null")
		return nil

	case NullLiteral:
		c.w.Write("null")
		return nil

	case BoolLiteral:
		if v.Value {
			c.w.Write("true")
		} else {
			c.w.Write("false")
		}
		return nil

	case NumberLiteral:
		c.w.Bind(v.Value)
		return nil

	case StringLiteral:
		c.w.Bind(v.Value)
		return nil

	case BigIntLiteral:
		c.w.Bind(v.Value)
		c.w.Write("::bigint")
		return nil

	case NumericLiteral:
		c.w.Bind(v.Value)
		c.w.Write("::numeric")
		return nil

	case DefaultKeyword:
		// Only ever reached via compileColumnRead ; reaching this generic
		// case is a codegen bug, not a user-facing error.
		return fmt.Errorf("sql: DefaultKeyword reached outside a get/get-set default position")

	case Star:
		return fmt.Errorf("sql: \"*\" is not a value-position expression")

	case *Identifier:
		return c.compileResolvedField(v.Resolved, n)

	case UnaryExpr:
		return c.compileUnary(v, n)

	case BinaryExpr:
		return c.compileBinary(v, n)

	case FoldedExpr:
		return c.compileFolded(v, n)

	case BetweenExpr:
		if err := c.compileOperand(v.Exp, n); err != nil {
			return err
		}
		if v.Negate {
			c.w.Write(" not between ")
		} else {
			c.w.Write(" between ")
		}
		if err := c.compileOperand(v.Min, n); err != nil {
			return err
		}
		c.w.Write(" and ")
		return c.compileOperand(v.Max, n)

	case InExpr:
		return c.compileIn(v, n)

	case AnyAllExpr:
		return c.compileAnyAll(v, n)

	case ConcatWsExpr:
		c.w.Write("concat_ws")
		return c.compileArgList(append([]Expression{v.Separator}, v.Args...), n)

	case CoalesceExpr:
		c.w.Write("coalesce")
		return c.compileArgList(v.Args, n)

	case FormatExpr:
		c.w.Write("format")
		return c.compileArgList(append([]Expression{StringLiteral{Value: v.Format}}, v.Args...), n)

	case *AggExpr:
		return c.compileAgg(v, n)

	case *CallExpr:
		if v.ResolvedFunction == nil {
			return fmt.Errorf("sql: call to %q has no resolved function", v.Identifier.Name)
		}
		c.w.Write(v.ResolvedFunction.Identifier.EscapedString())
		return c.compileArgList(v.Arguments, n)

	case ObjectExpr:
		return c.compileObjectLiteral(v.Fields, n)

	case OwnExpr, FullExpr, OwnExceptExpr, FullExceptExpr, OwnAndExpr, FullAndExpr, OwnExceptAndExpr, FullExceptAndExpr:
		// A shape-producing construct reached as a nested VALUE, not a
		// node's own top-level select — built as one jsonb value instead.
		return c.compileShapeAsJsonObject(e, n)

	case ArrExpr:
		return c.compileArray(v.Items, n)

	case LstExpr:
		return c.compileArray(v.Items, n)

	case IndexExpr:
		if err := c.compileOperand(v.Array, n); err != nil {
			return err
		}
		c.w.Write("[")
		if err := c.compileExpr(v.Index, n); err != nil {
			return err
		}
		c.w.Write("]")
		return nil

	case SliceExpr:
		if err := c.compileOperand(v.Array, n); err != nil {
			return err
		}
		c.w.Write("[")
		if err := c.compileExpr(v.From, n); err != nil {
			return err
		}
		c.w.Write(":")
		if err := c.compileExpr(v.To, n); err != nil {
			return err
		}
		c.w.Write("]")
		return nil

	case *GetSetExpr:
		return c.compileColumnRead(v.ResolvedColumn, n, v.DefaultGet)

	case *GetExpr:
		return c.compileColumnRead(v.ResolvedColumn, n, v.DefaultValue)

	case *SetExpr:
		// Write-only : never fetched in query mode (query.ts's own note).
		c.w.Write("null")
		return nil

	case ParamExpr:
		// A named placeholder resolved later ; cast defaults to jsonb, not
		// text (well-known-queries.md ## Definition).
		c.w.BindParam(v.Name)
		cast := v.Cast
		if cast == "" {
			cast = "jsonb"
		}
		c.w.Write("::")
		c.w.Write(cast)
		return nil

	default:
		return fmt.Errorf("sql: no codegen case for %T", e)
	}
}

// compileResolvedField emits whatever an *Identifier resolved to. n
// distinguishes a self-reference (r == n, ## Scoping) from another node.
func (c *sqlCompiler) compileResolvedField(field ResolvedField, n *QueryNode) error {
	switch r := field.(type) {
	case ColumnPath:
		return c.compileColumnPath(r, n)
	case *QueryNode:
		if r == n {
			// c.alias[node] is set before its own select list compiles
			// (compileNodeCorrelated), so this is always already known.
			alias, ok := c.alias[r]
			if !ok {
				return fmt.Errorf("sql: self-reference resolved to a node with no known alias yet (compiler ordering bug)")
			}
			c.w.Write(alias)
			return nil
		}
		// r is always a direct child of n (LookupInScope's own scope rule).
		return c.compileChildRowValue(r, n)
	case Shape:
		return fmt.Errorf("sql: a literal-object landing reached as a bare expression value is not yet supported")
	case ComputedFieldRef:
		return c.compileComputedFieldRef(r, n)
	default:
		return fmt.Errorf("sql: identifier resolved to nothing (opaque), cannot compile as a value")
	}
}

// compileComputedFieldRef emits r as a plain function call over whatever
// row it's bound to : "schema.fn(alias)" for a same-node reference,
// delegating to compileScalarHopComputed for one reached by hopping into a
// to-one child (r.Node != n) — the same split compileColumnPath makes
// between a same-node column and one reached via compileScalarHop.
func (c *sqlCompiler) compileComputedFieldRef(r ComputedFieldRef, n *QueryNode) error {
	if r.Node != n {
		return c.compileScalarHopComputed(r, n)
	}
	alias, ok := c.alias[r.Node]
	if !ok {
		return fmt.Errorf("sql: no alias assigned for node owning computed field %q — compiled out of order", r.Function.Identifier.Name)
	}
	c.w.Write(r.Function.Identifier.EscapedString())
	c.w.Paren(func() { c.w.Write(alias) })
	return nil
}

// compileScalarHopComputed is compileScalarHop's counterpart for a "."
// hop landing on a to-one child's own computed field — same correlated
// scalar-subquery shape, selecting a function call over the child's row
// instead of a column.
func (c *sqlCompiler) compileScalarHopComputed(r ComputedFieldRef, n *QueryNode) error {
	child := r.Node
	if !isOutgoingOf(child.Parent, child) {
		return fmt.Errorf("sql: %q is a to-many relation — a \".\" hop can only be compiled as a value when it reaches a to-one relation, since there is no single row to pick a field from otherwise (aggregate it with \"agg\" instead)", child.OuterAlias)
	}
	alias := c.allocAlias()

	var innerErr error
	c.w.Paren(func() {
		c.w.Write("select ")
		c.w.Write(r.Function.Identifier.EscapedString())
		c.w.Paren(func() { c.w.Write(alias) })
		c.w.Write(" from ")
		if err := c.compileFrom(child, alias); err != nil {
			innerErr = err
			return
		}
		if err := c.compileScalarHopWhere(child, alias, n); err != nil {
			innerErr = err
		}
	})
	return innerErr
}

// compileColumnPath emits a qualified column, or "(t.a).b" for a composite
// sub-field ; cp.Node != n delegates to compileScalarHop instead.
func (c *sqlCompiler) compileColumnPath(cp ColumnPath, n *QueryNode) error {
	if cp.Node != n {
		return c.compileScalarHop(cp, n)
	}
	alias, ok := c.alias[cp.Node]
	if !ok {
		return fmt.Errorf("sql: no alias assigned for node owning column %q — compiled out of order", cp.Path[0].Name)
	}
	c.compileColumnPathN(alias, cp.Path, len(cp.Path)-1)
	return nil
}

func (c *sqlCompiler) compileColumnPathN(alias string, path []*pg.Column, i int) {
	writeQualifiedPathN(c.w, alias, path, i)
}

// compileScalarHop compiles cp (a "." hop onto a to-one relation other than
// n) as its own scalar correlated subquery ; repeated hops aren't deduplicated.
func (c *sqlCompiler) compileScalarHop(cp ColumnPath, n *QueryNode) error {
	child := cp.Node
	if !isOutgoingOf(child.Parent, child) {
		// Enforced here, not at resolution (resolveHopInto allows any child) :
		// a to-many landing has no single row to pick a field from — use "agg".
		return fmt.Errorf("sql: %q is a to-many relation — a \".\" hop can only be compiled as a value when it reaches a to-one relation, since there is no single row to pick a field from otherwise (aggregate it with \"agg\" instead)", child.OuterAlias)
	}
	alias := c.allocAlias()

	var innerErr error
	c.w.Paren(func() {
		c.w.Write("select ")
		c.compileColumnPathN(alias, cp.Path, len(cp.Path)-1)
		c.w.Write(" from ")
		if err := c.compileFrom(child, alias); err != nil {
			innerErr = err
			return
		}
		if err := c.compileScalarHopWhere(child, alias, n); err != nil {
			innerErr = err
		}
	})
	return innerErr
}

// compileChildRowValue compiles a bare reference to a to-one child's own
// alias — compileScalarHop's counterpart, selecting the whole row.
func (c *sqlCompiler) compileChildRowValue(child *QueryNode, n *QueryNode) error {
	if !isOutgoingOf(child.Parent, child) {
		return fmt.Errorf("sql: %q is a to-many relation — a child's own alias can only be compiled as a bare value when it's a to-one relation, since there is no single row to reference otherwise (aggregate it with \"agg\" instead)", child.OuterAlias)
	}
	alias := c.allocAlias()

	var innerErr error
	c.w.Paren(func() {
		c.w.Write("select ")
		c.w.Write(alias)
		c.w.Write(" from ")
		if err := c.compileFrom(child, alias); err != nil {
			innerErr = err
			return
		}
		if err := c.compileScalarHopWhere(child, alias, n); err != nil {
			innerErr = err
		}
	})
	return innerErr
}

// compileScalarHopWhere is compileWhere's scalar-hop counterpart : each
// level here is its own subquery, never the same flat FROM-list scope.
func (c *sqlCompiler) compileScalarHopWhere(child *QueryNode, alias string, n *QueryNode) error {
	if len(child.JoinColumns) == 0 {
		// query.ts : `on` is mandatory on joined relations ; zero here means
		// an earlier pass let an invalid tree through uncaught.
		return fmt.Errorf("sql: %q has no join columns to correlate a \".\" hop by — compiled out of order", child.OuterAlias)
	}
	c.w.Write(" where ")
	for i, jc := range child.JoinColumns {
		if i > 0 {
			c.w.Write(" and ")
		}
		c.qualify(alias, jc.Local.Name)
		c.w.Write(" = ")
		distant := ColumnPath{Node: child.Parent, Path: []*pg.Column{jc.Distant}}
		if err := c.compileColumnPath(distant, n); err != nil {
			return err
		}
	}
	if child.Where != nil {
		c.w.Write(" and ")
		if err := c.compileOperand(child.Where, child); err != nil {
			return err
		}
	}
	return nil
}

// ---- operators --------------------------------------------------------------------

// compileOperand parenthesizes e when it's a BinaryExpr/FoldedExpr, never
// otherwise — no precedence table, trading redundant parens for safety.
func (c *sqlCompiler) compileOperand(e Expression, n *QueryNode) error {
	switch e.(type) {
	case BinaryExpr, FoldedExpr:
		var err error
		c.w.Paren(func() {
			err = c.compileExpr(e, n)
		})
		return err
	default:
		return c.compileExpr(e, n)
	}
}

func (c *sqlCompiler) compileUnary(v UnaryExpr, n *QueryNode) error {
	switch v.Op {
	case UnaryNeg:
		c.w.Write("-")
		return c.compileOperand(v.Expr, n)
	case UnaryNot:
		c.w.Write("not ")
		return c.compileOperand(v.Expr, n)
	case UnaryBitNot:
		c.w.Write("~")
		return c.compileOperand(v.Expr, n)
	case UnarySqrt:
		c.w.Write("|/")
		return c.compileOperand(v.Expr, n)
	case UnaryCubeRoot:
		c.w.Write("||/")
		return c.compileOperand(v.Expr, n)
	case UnaryIsNull:
		return c.compilePostfix(v.Expr, n, " is null")
	case UnaryIsTrue:
		return c.compilePostfix(v.Expr, n, " is true")
	case UnaryIsFalse:
		return c.compilePostfix(v.Expr, n, " is false")
	case UnaryIsNotNull:
		return c.compilePostfix(v.Expr, n, " is not null")
	case UnaryIsNotTrue:
		return c.compilePostfix(v.Expr, n, " is not true")
	case UnaryIsNotFalse:
		return c.compilePostfix(v.Expr, n, " is not false")
	default:
		return fmt.Errorf("sql: unknown unary operator %q", v.Op)
	}
}

func (c *sqlCompiler) compilePostfix(e Expression, n *QueryNode, suffix string) error {
	if err := c.compileOperand(e, n); err != nil {
		return err
	}
	c.w.Write(suffix)
	return nil
}

// foldOpTextOverrides maps the few operator constants that aren't already
// their own Postgres spelling ; everything else reuses query.ts's own token.
var foldOpTextOverrides = map[FoldedOperator]string{
	FoldAnd:               " and ",
	FoldOr:                " or ",
	FoldIsDistinctFrom:    " is distinct from ",
	FoldIsNotDistinctFrom: " is not distinct from ",
}

func (c *sqlCompiler) compileFolded(v FoldedExpr, n *QueryNode) error {
	switch v.Op {
	case FoldDot:
		// Resolved by pass 2 into a ColumnPath on Right ; collapses to one
		// composite-path emission, never a recursive Left/Right compile.
		id, ok := v.Right.(*Identifier)
		if !ok {
			return fmt.Errorf("sql: \".\" hop's right side is not an identifier (%T)", v.Right)
		}
		return c.compileResolvedField(id.Resolved, n)

	case FoldCoalesceAlias:
		c.w.Write("coalesce")
		return c.compileArgList([]Expression{v.Left, v.Right}, n)

	case FoldConcatCoalescing:
		// Postgres's own concat() already treats NULL as '' — query.ts's
		// own note, no hand-rolled coalescing needed.
		c.w.Write("concat")
		return c.compileArgList([]Expression{v.Left, v.Right}, n)

	default:
		opText, ok := foldOpTextOverrides[v.Op]
		if !ok {
			opText = " " + string(v.Op) + " "
		}
		if err := c.compileOperand(v.Left, n); err != nil {
			return err
		}
		c.w.Write(opText)
		return c.compileOperand(v.Right, n)
	}
}

func (c *sqlCompiler) compileBinary(v BinaryExpr, n *QueryNode) error {
	if v.Op == BinaryCast {
		if err := c.compileOperand(v.Left, n); err != nil {
			return err
		}
		typeName, err := castTypeName(v.Right)
		if err != nil {
			return err
		}
		c.w.Write("::")
		c.w.Write(typeName)
		return nil
	}
	if err := c.compileOperand(v.Left, n); err != nil {
		return err
	}
	c.w.Write(" ")
	c.w.Write(string(v.Op))
	c.w.Write(" ")
	return c.compileOperand(v.Right, n)
}

// validCastTypeName matches Postgres type-name syntax : space-separated
// words, an optional (precision[,scale]), any number of trailing "[]".
var validCastTypeName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\s+[a-zA-Z_][a-zA-Z0-9_]*)*(\(\s*\d+(\s*,\s*\d+)?\s*\))?(\s*\[\s*\])*$`)

// castTypeName extracts a "::" cast's type name ; left unresolved by pass 2,
// so this is the only gate against writing unvetted user JSON into SQL.
func castTypeName(e Expression) (string, error) {
	var name string
	switch v := e.(type) {
	case *Identifier:
		name = v.Name
	case StringLiteral:
		name = v.Value
	default:
		return "", fmt.Errorf("sql: unsupported cast target %T — expected a bare type name", e)
	}
	if !validCastTypeName.MatchString(name) {
		return "", fmt.Errorf("sql: invalid cast type name %q", name)
	}
	return name, nil
}

func (c *sqlCompiler) compileIn(v InExpr, n *QueryNode) error {
	if err := c.compileOperand(v.Subject, n); err != nil {
		return err
	}
	if v.Negate {
		c.w.Write(" not in ")
	} else {
		c.w.Write(" in ")
	}
	var err error
	c.w.Paren(func() {
		for i, cand := range v.Candidates {
			if i > 0 {
				c.w.Write(", ")
			}
			if cand.IsLiteral {
				c.w.Bind(cand.Literal)
				continue
			}
			if e := c.compileExpr(cand.Expr, n); e != nil {
				err = e
				return
			}
		}
	})
	return err
}

func (c *sqlCompiler) compileAnyAll(v AnyAllExpr, n *QueryNode) error {
	if err := c.compileOperand(v.Subject, n); err != nil {
		return err
	}
	opText, ok := foldOpTextOverrides[FoldedOperator(v.Op)]
	if ok {
		c.w.Write(opText[:len(opText)-1]) // drop the trailing space, " any"/"all" adds its own
	} else {
		c.w.Write(" ")
		c.w.Write(v.Op)
	}
	if v.All {
		c.w.Write(" all")
	} else {
		c.w.Write(" any")
	}
	var err error
	c.w.Paren(func() {
		if e := c.compileExpr(v.Array, n); e != nil {
			err = e
			return
		}
		// A literal array needs an explicit element cast : Postgres's $N
		// inference under "= ANY(...)" defaults to text otherwise.
		switch v.Array.(type) {
		case ArrExpr, LstExpr:
			if typ := subjectPgType(v.Subject); typ != nil {
				c.w.Write("::")
				c.w.Write(typ.PgIdentifier.EscapedString())
				c.w.Write("[]")
			}
		}
	})
	return err
}

// subjectPgType returns e's Postgres type when it's a plain column
// reference, nil otherwise ; used to cast a literal array's element type.
func subjectPgType(e Expression) *pg.Type {
	id, ok := e.(*Identifier)
	if !ok {
		return nil
	}
	cp, ok := id.Resolved.(ColumnPath)
	if !ok {
		return nil
	}
	return cp.CurrentType()
}

// ---- arg lists / function calls ----------------------------------------------------

func (c *sqlCompiler) compileArgList(args []Expression, n *QueryNode) error {
	var err error
	writer.SurroundList(c.w.Writer, "(", ", ", ")", args, func(a Expression) {
		if err != nil {
			return
		}
		err = c.compileExpr(a, n)
	})
	return err
}

func (c *sqlCompiler) compileExprParenList(args []Expression, n *QueryNode) error {
	return c.compileArgList(args, n)
}

// compileAgg emits one "agg" reference : the LATERAL wrapper's column if
// shared (analyzeLaterals), otherwise its own correlated scalar subquery.
func (c *sqlCompiler) compileAgg(v *AggExpr, n *QueryNode) error {
	if v.ResolvedFunction == nil {
		return fmt.Errorf("sql: agg %q has no resolved function", v.Identifier.Name)
	}
	target := aggTargetChild(v)
	if target == nil {
		return fmt.Errorf("sql: agg %q's argument doesn't reference an incoming relation (query.ts : the expression to aggregate must be an incoming relation)", v.Identifier.Name)
	}

	if plan, shared := c.laterals[target]; shared {
		for _, a := range plan.aggs {
			if a.expr == v {
				c.w.Write(plan.alias)
				c.w.Write(".")
				c.w.Write(a.alias)
				return nil
			}
		}
		return fmt.Errorf("sql: internal error : agg not found in its own node's LATERAL plan")
	}

	parentAlias, ok := c.alias[n]
	if !ok {
		return fmt.Errorf("sql: no alias assigned for node — compiled out of order")
	}
	childAlias := c.allocAlias()
	c.alias[target] = childAlias

	var err error
	c.w.Paren(func() {
		c.w.Write("select ")
		if e := c.compileAggFunctionCall(v, target, childAlias); e != nil {
			err = e
			return
		}
		c.w.Write(" from (\n")
		c.w.Indented(func() {
			if e := c.compileNodeCorrelated(target, childAlias, parentAlias); e != nil {
				err = e
			}
		})
		c.w.Write("\n) ")
		c.w.Write(childAlias)
	})
	return err
}

// compileAggFunctionCall emits only "fn(args...) filter (where ...)" ; used
// by compileLateralJoin too, never compileAgg, to avoid re-triggering the shared-vs-not lookup.
func (c *sqlCompiler) compileAggFunctionCall(v *AggExpr, target *QueryNode, targetAlias string) error {
	c.w.Write(v.ResolvedFunction.Identifier.EscapedString())
	var err error
	writer.SurroundList(c.w.Writer, "(", ", ", ")", v.Arguments, func(a Expression) {
		if err != nil {
			return
		}
		// A bare reference to the target relation (not a "." chain into a
		// column) has no ColumnPath ; emit "t.*", Postgres's own row value.
		if qn, isBare := a.(*Identifier); isBare {
			if resolved, _ := qn.Resolved.(*QueryNode); resolved == target {
				c.w.Write(targetAlias)
				c.w.Write(".*")
				return
			}
		}
		err = c.compileExpr(a, target)
	})
	if err != nil {
		return err
	}
	if v.Filter != nil {
		c.w.Write(" filter ")
		c.w.Paren(func() {
			c.w.Write("where ")
			if e := c.compileExpr(v.Filter, target); e != nil {
				err = e
			}
		})
	}
	return err
}

func (c *sqlCompiler) compileFunctionCall(fn *pg.Function, positional []Expression, named map[string]Expression, argScope *QueryNode) error {
	c.w.Write(fn.Identifier.EscapedString())
	return c.compileFunctionArgs(positional, named, argScope)
}

// compileFunctionArgs writes positional or named ("arg_name => value")
// call arguments ; used for both a table-valued call and a scalar one.
func (c *sqlCompiler) compileFunctionArgs(positional []Expression, named map[string]Expression, argScope *QueryNode) error {
	if named != nil {
		keys := make([]string, 0, len(named))
		for k := range named {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var err error
		c.w.Paren(func() {
			for i, k := range keys {
				if i > 0 {
					c.w.Write(", ")
				}
				c.w.Write(k)
				c.w.Write(" => ")
				if e := c.compileExpr(named[k], argScope); e != nil {
					err = e
					return
				}
			}
		})
		return err
	}
	return c.compileArgList(positional, argScope)
}

// ---- object literals / own-full-as-value / arrays ----------------------------------

func (c *sqlCompiler) compileObjectLiteral(fields map[string]Expression, n *QueryNode) error {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	c.w.Write("jsonb_build_object")
	var err error
	c.w.Paren(func() {
		for i, k := range keys {
			if i > 0 {
				c.w.Write(", ")
			}
			c.w.Bind(k)
			c.w.Write("::text, ")
			if e := c.compileExpr(fields[k], n); e != nil {
				err = e
				return
			}
		}
	})
	return err
}

// compileShapeAsJsonObject builds one jsonb value for own/full reached as
// a nested value ; reuses selectFieldsFor so both agree on each variant.
func (c *sqlCompiler) compileShapeAsJsonObject(e Expression, n *QueryNode) error {
	saved := n.Select
	n.Select = e
	fields, err := selectFieldsFor(n)
	n.Select = saved
	if err != nil {
		return err
	}

	c.w.Write("jsonb_build_object")
	c.w.Paren(func() {
		for i, f := range fields {
			if i > 0 {
				c.w.Write(", ")
			}
			c.w.Bind(f.key)
			c.w.Write("::text, ")
			if e := c.compileSelectFieldValue(n, f); e != nil {
				err = e
				return
			}
		}
	})
	return err
}

// compileSelectFieldValue is compileSelectField minus the "AS key" suffix ;
// compileShapeAsJsonObject binds the key as a jsonb_build_object argument instead.
func (c *sqlCompiler) compileSelectFieldValue(n *QueryNode, f selectField) error {
	alias, ok := c.alias[n]
	if !ok {
		return fmt.Errorf("sql: no alias assigned for node — compiled out of order")
	}
	return c.compileSelectField(n, alias, f)
}

func (c *sqlCompiler) compileArray(items []Expression, n *QueryNode) error {
	c.w.Write("array")
	var err error
	c.w.Surround("[", "]", func() {
		for i, item := range items {
			if i > 0 {
				c.w.Write(", ")
			}
			if e := c.compileExpr(item, n); e != nil {
				err = e
				return
			}
		}
	})
	return err
}

// ---- get / get-set --------------------------------------------------------------

// compileColumnRead emits col, or col coalesced with def ; DefaultKeyword
// splices pg.Column.DefaultExpression verbatim (from pg_attrdef).
func (c *sqlCompiler) compileColumnRead(col *pg.Column, n *QueryNode, def Expression) error {
	if col == nil {
		return fmt.Errorf("sql: get/get-set has no resolved column")
	}
	alias, ok := c.alias[n]
	if !ok {
		return fmt.Errorf("sql: no alias assigned for node owning column %q — compiled out of order", col.Name)
	}
	if def == nil {
		c.qualify(alias, col.Name)
		return nil
	}
	c.w.Write("coalesce")
	var err error
	c.w.Paren(func() {
		c.qualify(alias, col.Name)
		c.w.Write(", ")
		if _, isDefault := def.(DefaultKeyword); isDefault {
			if col.DefaultExpression == "" {
				err = fmt.Errorf("sql: \"default\" keyword used on column %q, which has no DB-level default", col.Name)
				return
			}
			c.w.Write(col.DefaultExpression)
			return
		}
		err = c.compileExpr(def, n)
	})
	return err
}
