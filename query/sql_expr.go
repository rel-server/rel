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
// against (needed for own/full/except/and's implicit column list, and to
// know which alias a bare, self-referencing column belongs to when e itself
// carries no ColumnPath.Node of its own — e.g. an "own" field's plain
// pg.Column).
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
		// Only ever reached as a GetExpr/GetSetExpr default position, handled
		// directly by compileColumnRead — reaching this generic case means a
		// caller forwarded it somewhere else, which is a codegen bug, not a
		// user-facing error.
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
		// A shape-producing construct reached as a VALUE (nested inside
		// another expression, e.g. an object literal field), as opposed to
		// being a node's own top-level select (compileSelectList's job) :
		// build it the same way, as a single jsonb value.
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
		return fmt.Errorf("sql: $param (well-known query parameters) are not yet supported by codegen")

	default:
		return fmt.Errorf("sql: no codegen case for %T", e)
	}
}

// compileResolvedField emits whatever an *Identifier resolved to — the
// common landing spot for both a first-hop scope lookup and the terminus of
// a "." chain (see resolveChain/resolveHopInto, expression_resolve.go). n is
// the node whose own expression is currently being compiled — needed to
// distinguish a SELF-reference (r == n, e.g. "property" used as the bare
// argument to a row-type-taking computed column, property_average_rating
// (property) — specs/query-engine.md's "## Scoping"'s computed-column paragraph)
// from a reference to some OTHER node (a child/sibling/ancestor), which
// stays unsupported below.
func (c *sqlCompiler) compileResolvedField(field ResolvedField, n *QueryNode) error {
	switch r := field.(type) {
	case ColumnPath:
		return c.compileColumnPath(r, n)
	case *QueryNode:
		if r == n {
			// A self-reference's own alias is always in scope here : it was
			// recorded into c.alias BEFORE this node's own select-list
			// expressions ever started compiling (compileNodeCorrelated sets
			// c.alias[node] first thing, then emits the select list) — so
			// the row this expression is nested inside of is exactly the
			// row named by c.alias[r]. Emitting that bare alias is a valid,
			// correlated reference to the node's own current row — exactly
			// what a Postgres function taking that row's own composite type
			// as an argument expects.
			alias, ok := c.alias[r]
			if !ok {
				return fmt.Errorf("sql: self-reference resolved to a node with no known alias yet (compiler ordering bug)")
			}
			c.w.Write(alias)
			return nil
		}
		// A DIFFERENT node embedded as a bare value : LookupInScope's own
		// scope rule (scope.go) means r is always a DIRECT child of n here
		// — "never a parent's or a sibling's" — so this is always r.Parent
		// == n, never an ancestor or cousin. Compiled the same way a "."
		// hop's scalar landing is (compileScalarHop, below) : a self-
		// contained correlated subquery, except selecting the child's own
		// row alias directly instead of one of its columns, since the
		// whole row (as a composite value) is what was asked for here, not
		// a single field.
		return c.compileChildRowValue(r, n)
	case Shape:
		return fmt.Errorf("sql: a literal-object landing reached as a bare expression value is not yet supported")
	default:
		return fmt.Errorf("sql: identifier resolved to nothing (opaque), cannot compile as a value")
	}
}

// compileColumnPath emits the SQL text for a ColumnPath — a plain qualified
// column reference (len(Path)==1), or Postgres's row-value parenthesization
// for a composite sub-field (len(Path)>1) : "t.a.b" isn't legal syntax for
// "column a, sub-field b", it must be "(t.a).b", and further nested,
// "((t.a).b).c". Built recursively (innermost first) : compileColumnPathN
// emits Path[:i+1], wrapping the i-1 prefix in Paren before appending
// Path[i]'s own ".name" — Paren's callback-based shape is exactly what
// makes building this inside-out correct without re-emitting anything.
//
// n is the node currently being compiled. cp.Node == n is the ordinary
// same-scope case above. cp.Node != n only ever happens via a "." hop
// through a to-one child (resolveHopInto's own cardinality check rules out
// a to-many landing at resolve time — see expression_resolve.go) — compiled
// as its own self-contained scalar correlated subquery by compileScalarHop,
// never by trusting c.alias[cp.Node] : that map entry, if one even exists,
// may belong to a DIFFERENT, already-closed embed subquery elsewhere in
// this same select list (c.alias is never cleared when a subquery closes),
// and reusing it here would silently emit a reference to a table alias
// out of scope at this point in the statement — invalid SQL, not merely a
// stale-but-harmless lookup. Building a fresh, independent subquery here
// sidesteps that risk entirely, at the cost of not sharing a scan across
// two separate "." hops into the same child (see compileScalarHop's own
// doc comment).
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

// compileScalarHop compiles cp — a "." hop landing on a to-one relation
// that isn't n, the node currently being compiled — as its own
// self-contained scalar correlated subquery : "(select <col-expr> from
// <relation> <fresh alias> where <on-clause> and <that relation's own
// where>)". Structurally the same shape a to-one embed gets
// (compileEmbedField, sql.go), just selecting one specific (possibly
// composite-nested) column instead of row_to_json(alias) ; and, unlike an
// embed, deliberately bypassing cp.Node's own Select/selectFieldsFor/
// laterals machinery entirely — cp.Node's own `select` (if it has one)
// governs what gets embedded when the SAME node is ALSO selected as a full
// embed elsewhere in the query, a wholly separate concern from picking a
// single column here.
//
// Recurses through more than one to-one hop (e.g. movie -> director ->
// studio) one nested subquery per level, via compileScalarHopWhere calling
// back into compileColumnPath for the parent-linking column — no separate
// "is this more than one level away" branch needed, since compileColumnPath
// itself already is the cp.Node == n / cp.Node != n dispatch.
//
// order by/limit/distinct are deliberately never compiled for cp.Node here
// : cp.Node is guaranteed to-one (resolveHopInto's own cardinality check,
// backed by pg.Relation.ResolveJoin's uniqueness requirement — query-engine.md
// ### Join eligibility), so at most one row can ever match regardless of
// ordering ; there's nothing for them to do.
//
// Not deduplicated against another "." hop into the same relation
// elsewhere in the same select list — each hop compiles its own
// independent subquery, so N references execute N separate scans. A
// LATERAL-sharing optimization mirroring analyzeLaterals' existing one for
// multiply-consumed incoming children would remove this, but isn't
// implemented here — flagged as a known, deliberate limitation of this
// first pass, not a silent inefficiency nobody noticed.
func (c *sqlCompiler) compileScalarHop(cp ColumnPath, n *QueryNode) error {
	child := cp.Node
	if !isOutgoingOf(child.Parent, child) {
		// Resolution deliberately allows a "." hop into ANY child (see
		// expression_resolve.go's resolveHopInto doc comment) — this is
		// the actual, later-stage enforcement : compiling one as a plain
		// scalar value only makes sense when there's a single row to pick
		// it from. A to-many landing reaching this far means something
		// tried to use a "." hop's resolved value directly as a select
		// expression rather than aggregating it — same restriction "agg"
		// enforces in the opposite direction (query.ts : agg's target
		// "must be an incoming relation"), same late-compile-stage pattern
		// (aggTargetChild, this file).
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

// compileChildRowValue compiles a bare reference to a direct to-one child's
// own alias (e.g. `["coalesce", "director", null]`, "director" being a
// joined-in child's OuterAlias) — the *QueryNode-landing counterpart of
// compileScalarHop's ColumnPath one : same self-contained correlated
// subquery shape, same to-one-only restriction (child.Parent is always ==
// n here — see compileResolvedField's own call site), but selecting
// child's own row alias directly rather than compileColumnPathN-ing one of
// its columns, since what was asked for is the whole row as a composite
// value, not a single field. `select alias from schema.rel alias where
// ...` : selecting a table alias bare like this is what makes Postgres
// return the row's own composite type — exactly the value a function
// like coalesce() taking that row type as an argument expects, and a
// zero-row match (an optional to-one FK with nothing on the other end)
// yields SQL NULL automatically, same as any other scalar subquery.
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

// compileScalarHopWhere emits child's own "where" for a compileScalarHop
// subquery : its on-clause, correlated against n — recursively, through a
// nested compileScalarHop of its own, when child.Parent != n (more than
// one to-one hop away) — AND-ed with child's own declared `where`, if any.
// The scalar-hop counterpart of compileWhere (sql.go), which assumes its
// parent's alias is already in the very same flat FROM-list scope — never
// true here beyond the first hop, since every level of a scalar hop is its
// own separate, independently-scoped subquery.
func (c *sqlCompiler) compileScalarHopWhere(child *QueryNode, alias string, n *QueryNode) error {
	if len(child.JoinColumns) == 0 {
		// query.ts : `on` "is mandatory on joined relations" — a joined
		// child (child.Parent != nil) with no JoinColumns at all means an
		// earlier pass let an invalid tree through uncaught, not something
		// this function should paper over with a where-less subquery.
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

// compileOperand compiles e as a sub-expression operand : always
// parenthesized when e is itself a BinaryExpr/FoldedExpr, never otherwise —
// deliberately no operator-precedence table (see the plan this was designed
// under). A few harmless redundant parens in exchange for zero risk of a
// subtly wrong precedence rule, and no precedence-table testing surface at
// all.
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

// foldOpText/binaryOpText map an operator constant to its SQL infix text.
// Almost all constants ARE their own Postgres operator spelling already
// (query.ts deliberately reuses Postgres's own tokens where one exists) —
// only the entries below need translating.
var foldOpTextOverrides = map[FoldedOperator]string{
	FoldAnd:               " and ",
	FoldOr:                " or ",
	FoldIsDistinctFrom:    " is distinct from ",
	FoldIsNotDistinctFrom: " is not distinct from ",
}

func (c *sqlCompiler) compileFolded(v FoldedExpr, n *QueryNode) error {
	switch v.Op {
	case FoldDot:
		// Resolved away entirely by pass 2 into a ColumnPath on Right's
		// Identifier — the whole FoldedExpr collapses to one composite-path
		// emission, never a recursive Left/Right compile (Left is only ever
		// the static navigation prefix, not a value in its own right).
		id, ok := v.Right.(*Identifier)
		if !ok {
			return fmt.Errorf("sql: \".\" hop's right side is not an identifier (%T)", v.Right)
		}
		return c.compileResolvedField(id.Resolved, n)

	case FoldCoalesceAlias:
		c.w.Write("coalesce")
		return c.compileArgList([]Expression{v.Left, v.Right}, n)

	case FoldConcatCoalescing:
		// Postgres's own concat() already treats NULL as '' — exactly
		// query.ts's "coalesces individual operands with ''" note, no
		// hand-rolled coalescing needed on top of it.
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

// validCastTypeName matches Postgres type-name syntax : one or more
// space-separated identifier words (covers multi-word standard types like
// "character varying", "timestamp with time zone", "double precision"),
// an optional (precision[,scale]), and any number of trailing "[]".
var validCastTypeName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\s+[a-zA-Z_][a-zA-Z0-9_]*)*(\(\s*\d+(\s*,\s*\d+)?\s*\))?(\s*\[\s*\])*$`)

// castTypeName extracts a literal Postgres type name from a "::" cast's
// right-hand side. Pass 2 (expression_resolve.go's BinaryExpr case)
// special-cases BinaryCast the same way it does the jsonb operator family :
// Right is left unresolved, since it's a type name (e.g. "text", "int[]"),
// never a column/alias to scope-resolve. So Right always arrives here as
// whatever parseNode produced — a bare *Identifier for an unquoted type
// name, or a StringLiteral for a quoted one — straight from user JSON,
// unresolved and therefore unvetted by pass 2's usual scope check. It gets
// written into the SQL text verbatim (EscapeSQLId would mis-quote multi-word
// types like "timestamp with time zone" into garbage), so this function is
// the only gate standing between it and the query : validate against
// validCastTypeName rather than just passing it through.
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
		// A literal array (["arr"/"lst", ...bound values]) needs an
		// explicit element-type cast here : Postgres's parameter-type
		// inference for bare $N placeholders inside an ARRAY[...]
		// constructor doesn't reliably propagate back from the surrounding
		// "= ANY(...)" comparison the way it does for a plain "x = $1"
		// comparison, and silently defaults to text — verified directly
		// (an integer column compared via "= any(array[$1,$2])" with no
		// cast errors "operator does not exist: integer = text"). Casting
		// to the SUBJECT's own already-known type (when it's a plain
		// column) closes exactly that gap ; an array containing genuine
		// sub-expressions (column refs, not just bound literals) doesn't
		// need this at all, Postgres infers those fine.
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

// subjectPgType returns e's Postgres type when e is a plain column
// reference (an *Identifier landing on a ColumnPath) — nil otherwise. Used
// by compileAnyAll to cast a literal array's element type to match.
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

// compileAgg emits one "agg" reference — reached only from the CONSUMING
// node's own select-list (via compileExpr's *AggExpr case), never used to
// emit the aggregate's own definition inside a LATERAL wrapper (that's
// compileAggFunctionCall, called directly by compileLateralJoin instead,
// bypassing this shared-vs-not branch entirely — see its own comment for
// why the two must stay separate).
//
// Two cases : the target child is LATERAL-shared (analyzeLaterals found
// more than one consumer), in which case the value is already computed —
// just reference the lateral wrapper's own column for it ; otherwise this
// agg is the child's ONLY consumer, so it compiles as its own correlated
// scalar subquery, structurally identical to a to-one embed's row_to_json
// wrapping except aggregating instead (compileEmbedField, sql.go).
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

// compileAggFunctionCall emits ONLY the aggregate function call itself —
// "fn(args...) filter (where ...)" — with args compiled against target's
// alias. Used both by compileAgg's own non-shared subquery wrapper above
// and by compileLateralJoin (sql.go), which needs the bare function-call
// form for each agg it materializes — never compileAgg itself there, since
// that would re-trigger the shared-vs-not lookup for the very agg currently
// being DEFINED, not referenced.
func (c *sqlCompiler) compileAggFunctionCall(v *AggExpr, target *QueryNode, targetAlias string) error {
	c.w.Write(v.ResolvedFunction.Identifier.EscapedString())
	var err error
	writer.SurroundList(c.w.Writer, "(", ", ", ")", v.Arguments, func(a Expression) {
		if err != nil {
			return
		}
		// A bare reference to the target relation itself (as opposed to a
		// "." chain into one of its columns) means "this relation's rows" —
		// e.g. ["agg", "count", ["movies"]] for a plain row count — which
		// has no ColumnPath to compile ; emit the aliased subquery's own
		// row value instead ("t.*"), Postgres's own way to reference a
		// whole FROM-item's row.
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

// compileFunctionArgs writes a function-rooted node's own call arguments —
// positional or named (Postgres's "arg_name => value" syntax) — used both
// for a FROM-clause table-valued call and a bare scalar function root/embed.
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

// compileShapeAsJsonObject builds one jsonb value for a shape-producing
// construct (own/full and their -except/-and variants) reached as a VALUE —
// nested inside another expression — rather than as a node's own top-level
// select (compileSelectList's job, which emits plain columns instead, per
// sql.go). Reuses selectFieldsFor's own/full/except/and inventory logic
// against n (the resolving node), so both places agree on what each variant
// means.
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

// compileSelectFieldValue is compileSelectField (sql.go) minus the "AS key"
// suffix — used by compileShapeAsJsonObject, where the field's SQL key
// comes from a bound jsonb_build_object argument instead of an "AS" alias.
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

// compileColumnRead emits a plain column read, or — when def is present —
// that value coalesced with the default : DefaultKeyword means "the
// column's own DB-level default expression" (pg.Column.DefaultExpression,
// already-introspected raw SQL text, safe to splice verbatim — it came from
// pg_attrdef via pg's own introspection, never from the query payload),
// anything else compiles normally as the fallback value.
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
