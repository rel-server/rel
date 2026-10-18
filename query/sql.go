// Pass 3, SQL codegen : compiles an already-resolved *QueryNode tree (pass
// 1 + pass 2 — see node_resolve.go/expression_resolve.go/shape.go) into one
// executable SQL statement, per specs/query-engine.md's Reading Algorithm.
// Covers the read path only ; the Writing Algorithm (temp tables,
// extractors, phased DML) is separate, later work.
package query

import (
	"fmt"
	"slices"
	"sort"

	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/writer"
	"github.com/samber/oops"
)

// sqlCompiler carries state for one CompileSelect call : the writer, a
// monotonic alias counter, and a Node -> alias map for cross-node references.
type sqlCompiler struct {
	w         *writer.SQLWriter
	nextAlias int
	alias     map[*QueryNode]string

	// laterals is the current node's LATERAL-sharing plan (nil outside
	// compileNode's select-list emission) ; saved/restored per recursion.
	laterals map[*QueryNode]*lateralPlan

	// Set only by CompileSelectForDataNode, checked only when compiling
	// that exact node (never a descendant).
	dataScopeRoot   *QueryNode
	dataScopeNodeID int
	dataScopeAlias  string
	dataScopeTable  *tempTable

	// countMode is CompileCount's own flag : compileNodeCorrelated emits
	// "select 1" and skips distinct/order-by/limit-offset/lateral-join-into-FROM,
	// keeping FROM/WHERE identical to CompileSelect's (specs/complex-query.md ## count).
	countMode bool

	// fromCTE, when set, makes the root node read from that CTE instead of its
	// table : the response of a write built from the write statement's own
	// RETURNING rows (query/write_fast.go). orderByRowID adds the CTE's
	// __row_id as the default order, like the temp-row scope does.
	fromCTE      string
	orderByRowID bool

	// format is how the rows come out : JSON text built by Postgres, or records read by a ResultEncoder.
	format OutputFormat
}

func newSQLCompiler() *sqlCompiler {
	return &sqlCompiler{w: writer.NewSQL(), alias: map[*QueryNode]string{}}
}

func (c *sqlCompiler) allocAlias() string {
	c.nextAlias++
	return fmt.Sprintf("t%d", c.nextAlias)
}

// qualify writes "alias.name" (escaped) ; centralized since
// c.w.Write(alias).Write(".") returns a *Writer with no .Id (writer/pg.go).
func (c *sqlCompiler) qualify(alias, name string) {
	c.w.Write(alias)
	c.w.Write(".")
	c.w.Id(name)
}

// CompileSelect compiles root into one executable SQL statement returning
// one row per root row, each with a single "json" column produced by
// row_to_json — exactly parallel to how a to-one embed is wrapped (see
// compileEmbedField). The one exception is a genuinely scalar function
// root (Relation == nil, a bare non-composite return type) : its result
// contributes directly, with no row_to_json wrapping — Reading Algorithm
// step 6, specs/query-engine.md ## Response Shape. A function returning a
// single composite row (Relation != nil, not SETOF) is not this case ; it
// goes through the ordinary compileNode path below like any other node.
//
// The caller owns executing the statement and manually streaming the
// response's "["/","/"]" per ## Response Shape — this function's contract
// stops at "here is one valid, executable statement plus its bind args".
func CompileSelect(root *QueryNode) (*writer.SQLWriter, error) {
	return CompileSelectFormat(root, FormatJSON)
}

// CompileSelectFormat is CompileSelect producing rows in the given format. In FormatCBOR the one column is the row
// as a record (or, for a scalar select, the bare value), and a to-many embed an array of records.
func CompileSelectFormat(root *QueryNode, format OutputFormat) (*writer.SQLWriter, error) {
	c := newSQLCompiler()
	c.format = format

	if root.IsFunction() && !root.Function.ReturnsSet && root.Relation == nil {
		c.w.Write("select ")
		if err := c.compileFunctionCall(root.Function, root.FunctionArguments, root.FunctionArgumentMap, root.Parent); err != nil {
			return nil, err
		}
		return c.w, nil
	}

	alias := c.allocAlias()
	c.w.Write("select ").Write(c.wrapNodeAsValue(root, alias)).Write(" from (\n")
	c.w.Indent()
	err := c.compileNode(root, alias)
	c.w.Unindent()
	if err != nil {
		return nil, err
	}
	c.w.Write("\n) ").Write(alias)
	return c.w, nil
}

// CompileCount compiles root into "select count(*) from (<FROM/WHERE>) t" —
// the same FROM/JOIN/WHERE CompileSelect would emit for root (a WHERE
// referencing a join always compiles as its own correlated subquery,
// regardless of root's own select list — see compileAgg/compileScalarHop),
// but no SELECT projection, ORDER BY, LIMIT, or OFFSET. Rejects a genuinely
// scalar function root (specs/complex-query.md ## count only defines this
// for a relation-shaped query) — mirrors CompileSelect's own special case.
func CompileCount(root *QueryNode) (*writer.SQLWriter, error) {
	if root.IsFunction() && !root.Function.ReturnsSet && root.Relation == nil {
		return nil, oops.With("node", root.InnerName).Errorf("sql: count is not meaningful on a scalar function root")
	}

	c := newSQLCompiler()
	c.countMode = true
	alias := c.allocAlias()
	c.w.Write("select count(*) from (\n")
	c.w.Indent()
	err := c.compileNode(root, alias)
	c.w.Unindent()
	if err != nil {
		return nil, err
	}
	c.w.Write("\n) ").Write(alias)
	return c.w, nil
}

// CompileSelectForDataNode compiles root exactly like CompileSelect, with
// one addition : restricted to the rows this write request actually
// touched, via the temp table of its relation — nodeID is root's own assigned __node_id (see
// ExecuteWrite/WriteResult.NodeIDs), and only its root-level rows
// (__parent_id is null) are in scope. Used for the write-then-reread
// response (specs/query-engine.md ## Response Shape : the response is built by
// a separate, read-only statement issued after the write transaction's
// commit, never from inside it) — the temp table must already exist and hold this
// request's rows on the connection this statement later runs against.
//
// A function-rooted node is rejected : functions aren't writable
// (query/node.go), so there's never a temp row for one to scope against.
func CompileSelectForDataNode(root *QueryNode, nodeID int) (*writer.SQLWriter, error) {
	return CompileSelectForDataNodeFormat(root, nodeID, FormatJSON)
}

// CompileSelectForDataNodeFormat is CompileSelectForDataNode producing rows in the given format.
func CompileSelectForDataNodeFormat(root *QueryNode, nodeID int, format OutputFormat) (*writer.SQLWriter, error) {
	if root.IsFunction() {
		return nil, oops.With("node", root.InnerName).Errorf("sql: a function-rooted node can't be write-scoped — functions aren't writable")
	}

	t, err := newTempTable(root.Relation)
	if err != nil {
		return nil, err
	}
	c := newSQLCompiler()
	c.format = format
	c.dataScopeRoot = root
	c.dataScopeNodeID = nodeID
	c.dataScopeAlias = "__wq"
	c.dataScopeTable = t

	alias := c.allocAlias()
	c.w.Write("select ").Write(c.wrapNodeAsValue(root, alias)).Write(" from (\n")
	c.w.Indent()
	err = c.compileNode(root, alias)
	c.w.Unindent()
	if err != nil {
		return nil, err
	}
	c.w.Write("\n) ").Write(alias)
	return c.w, nil
}

// compileNode emits a plain multi-column SELECT, no row_to_json/json_agg
// wrapper ; onParentAlias is "" for the root, else folds JoinColumns into where.
func (c *sqlCompiler) compileNode(node *QueryNode, alias string) error {
	return c.compileNodeCorrelated(node, alias, "")
}

func (c *sqlCompiler) compileNodeCorrelated(node *QueryNode, alias string, onParentAlias string) error {
	c.alias[node] = alias

	laterals, err := c.analyzeLaterals(node)
	if err != nil {
		return err
	}
	prevLaterals := c.laterals
	c.laterals = laterals
	defer func() { c.laterals = prevLaterals }()

	c.w.Write("select ")
	if c.countMode {
		c.w.Write("1")
	} else {
		if node.Distinct {
			c.w.Write("distinct ")
		} else if len(node.DistinctOn) > 0 {
			c.w.Write("distinct on ")
			if err := c.compileExprParenList(node.DistinctOn, node); err != nil {
				return err
			}
			c.w.Write(" ")
		}

		if isShapeProducingSelect(node.Select) {
			fields, err := selectFieldsFor(node)
			if err != nil {
				return err
			}
			if len(fields) == 0 {
				return oops.With("node", node.InnerName).Errorf("sql: node has no select fields to emit")
			}
			for i, f := range fields {
				if i > 0 {
					c.w.Write(", ")
				}
				if err := c.compileSelectField(node, alias, f); err != nil {
					return err
				}
				c.w.Write(" as ")
				c.w.Id(f.key)
			}
		} else {
			// ## Reading Algorithm ### Scalar-selected nodes : to_jsonb(...),
			// not a bare emission, since streamRows writes this column raw. In CBOR the value
			// comes out as it is, and its own type says how to encode it.
			if c.format == FormatCBOR {
				if err := c.compileExpr(node.Select, node); err != nil {
					return err
				}
				c.w.Write(" as __scalar")
			} else {
				c.w.Write("to_jsonb(")
				if err := c.compileExpr(node.Select, node); err != nil {
					return err
				}
				c.w.Write(") as __scalar")
			}
		}
	}

	c.w.Write(" from ")
	if err := c.compileFrom(node, alias); err != nil {
		return err
	}

	if !c.countMode {
		for _, child := range node.IncomingNodes {
			plan, shared := laterals[child]
			if !shared {
				continue
			}
			if err := c.compileLateralJoin(child, alias, plan); err != nil {
				return err
			}
		}
	}

	if err := c.compileWhere(node, alias, onParentAlias); err != nil {
		return err
	}
	if !c.countMode {
		if err := c.compileOrderBy(node); err != nil {
			return err
		}
		c.compileLimitOffset(node)
	}

	return nil
}

func (c *sqlCompiler) compileFrom(node *QueryNode, alias string) error {
	if node.IsFunction() {
		c.w.Write(node.Function.Identifier.EscapedString())
		if err := c.compileFunctionArgs(node.FunctionArguments, node.FunctionArgumentMap, node.Parent); err != nil {
			return err
		}
		c.w.Write(" ").Write(alias)
		return nil
	}
	if node.Relation == nil {
		return oops.With("node", node.InnerName).Errorf("sql: node has no relation to select from")
	}
	if c.fromCTE != "" && node.Parent == nil {
		c.w.Write(c.fromCTE).Write(" ").Write(alias)
		return nil
	}
	c.w.Write(node.Relation.Identifier.EscapedString()).Write(" ").Write(alias)
	if c.dataScopeRoot != nil && node == c.dataScopeRoot {
		t := c.dataScopeTable
		c.w.Write(" inner join ")
		c.w.Write(t.qualifiedGeneral())
		c.w.Write(" ")
		c.w.Write(c.dataScopeAlias)
		c.w.Write(" on ")
		c.w.Write(t.nodeIDRef(c.dataScopeAlias))
		c.w.Write(" = ")
		c.w.Write(fmt.Sprintf("%d", c.dataScopeNodeID))
		c.w.Write(" and ")
		c.w.Write(t.parentIDRef(c.dataScopeAlias))
		c.w.Write(" is null")
	}
	return nil
}

func (c *sqlCompiler) compileWhere(node *QueryNode, alias string, onParentAlias string) error {
	hasOn := onParentAlias != "" && len(node.JoinColumns) > 0
	hasDataScope := c.dataScopeRoot != nil && node == c.dataScopeRoot
	if !hasOn && !hasDataScope && node.Where == nil {
		return nil
	}
	c.w.Write(" where ")
	wrote := false
	if hasOn {
		for i, jc := range node.JoinColumns {
			if i > 0 {
				c.w.Write(" and ")
			}
			c.qualify(alias, jc.Local.Name)
			c.w.Write(" = ")
			c.qualify(onParentAlias, jc.Distant.Name)
		}
		wrote = true
	}
	if hasDataScope {
		if wrote {
			c.w.Write(" and ")
		}
		cols := identityColumns(node)
		if len(cols) == 0 {
			return oops.With("node", node.InnerName).Errorf("sql: has no identity (on_conflict or primary key) column set to scope the reread by")
		}
		for i, col := range cols {
			if i > 0 {
				c.w.Write(" and ")
			}
			c.qualify(alias, col.Name)
			c.w.Write(" = ")
			c.w.Write(c.dataScopeTable.colRef(c.dataScopeAlias, col))
		}
		wrote = true
	}
	if node.Where != nil {
		if wrote {
			c.w.Write(" and ")
		}
		if err := c.compileOperand(node.Where, node); err != nil {
			return err
		}
	}
	return nil
}

func (c *sqlCompiler) compileOrderBy(node *QueryNode) error {
	if len(node.OrderBy) == 0 {
		if c.dataScopeRoot != nil && node == c.dataScopeRoot {
			// Default to payload order only when the write query specified
			// no order_by of its own ; an explicit one is left untouched.
			c.w.Write(" order by ")
			c.w.Write(c.dataScopeTable.rowIDRef(c.dataScopeAlias))
		}
		if c.orderByRowID && node.Parent == nil {
			c.w.Write(" order by ")
			c.w.Write(c.alias[node])
			c.w.Write(".__row_id")
		}
		return nil
	}
	c.w.Write(" order by ")
	return c.compileOrderTerms(node.OrderBy, node)
}

// compileOrderTerms writes the comma-separated terms of an ORDER BY (a query's, or an aggregate's own).
func (c *sqlCompiler) compileOrderTerms(terms []OrderByTerm, node *QueryNode) error {
	for i, term := range terms {
		if i > 0 {
			c.w.Write(", ")
		}
		if err := c.compileExpr(term.Expr, node); err != nil {
			return err
		}
		switch term.Direction {
		case OrderDesc:
			// Postgres defaults DESC to NULLS FIRST, but query.ts promises
			// nulls-last for both asc and desc ; must say so explicitly.
			c.w.Write(" desc nulls last")
		case OrderAscNullsFirst:
			c.w.Write(" asc nulls first")
		case OrderDescNullsLast:
			c.w.Write(" desc nulls last")
		}
	}
	return nil
}

func (c *sqlCompiler) compileLimitOffset(node *QueryNode) {
	if node.Limit != nil {
		c.w.Write(" limit ")
		c.w.Bind(*node.Limit)
	}
	if node.Offset != nil {
		c.w.Write(" offset ")
		c.w.Bind(*node.Offset)
	}
}

// ---- select-field inventory -------------------------------------------------------

// selectField is one select-list entry : a physical column, a child alias,
// or a computed expression — separate from Shape.Fields's "." chaining targets.
type selectField struct {
	key    string
	column *pg.Column // set for a plain own/full column
	embed  *QueryNode // set for a full-included child alias
	expr   Expression // set for a computed field
}

// isShapeProducingSelect reports whether sel is a construct selectFieldsFor
// can expand ; anything else is a scalar select (### Scalar-selected nodes).
func isShapeProducingSelect(sel Expression) bool {
	switch sel.(type) {
	case StarExpr, ObjectExpr, *GetExpr:
		return true
	default:
		return false
	}
}

// wrapNodeAsValue writes node's already-compiled inner query as one value : row_to_json(innerAlias) in
// JSON, the row itself (a record) in CBOR, or innerAlias.__scalar for a scalar select.
func (c *sqlCompiler) wrapNodeAsValue(node *QueryNode, innerAlias string) string {
	if isShapeProducingSelect(node.Select) {
		if c.format == FormatCBOR {
			return innerAlias
		}
		return "row_to_json(" + innerAlias + ")"
	}
	return innerAlias + ".__scalar"
}

// aggregateRows writes the aggregate collecting the wrapped rows of a to-many embed : json_agg, or array_agg
// in CBOR, whose result (an array of records) is read as one value.
func (c *sqlCompiler) aggregateRows(wrapped string) string {
	if c.format == FormatCBOR {
		return "array_agg(" + wrapped + ")"
	}
	return "json_agg(" + wrapped + ")"
}

// emptyRows is what a to-many embed with no row is.
func (c *sqlCompiler) emptyRows() string {
	if c.format == FormatCBOR {
		return "'{}'"
	}
	return "'[]'::json"
}

// selectFieldsFor's switch enumerates the same type set isShapeProducingSelect
// checks ; kept adjacent so a new shape-producing case is added to both.
func selectFieldsFor(node *QueryNode) ([]selectField, error) {
	var fields []selectField

	addOwn := func(except []string) {
		if node.Relation == nil {
			return
		}
		excluded := make(map[string]bool, len(except))
		for _, e := range except {
			excluded[e] = true
		}
		for _, col := range node.Relation.Columns {
			if !excluded[col.Name] {
				fields = append(fields, selectField{key: col.Name, column: col})
			}
		}
	}
	addAliases := func() {
		for _, c := range node.OutgoingNodes {
			fields = append(fields, selectField{key: c.OuterAlias, embed: c})
		}
		for _, c := range node.IncomingNodes {
			fields = append(fields, selectField{key: c.OuterAlias, embed: c})
		}
	}
	addAnd := func(and map[string]Expression) {
		keys := make([]string, 0, len(and))
		for k := range and {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fields = append(fields, selectField{key: k, expr: and[k]})
		}
	}

	switch v := node.Select.(type) {
	case StarExpr:
		addOwn(v.Except)
		if !v.Own {
			addAliases()
		}
		addAnd(v.And)
	case ObjectExpr:
		addAnd(v.Fields)
	case *GetExpr:
		fields = append(fields, selectField{key: v.ResolvedColumn.Name, expr: v})
	default:
		// A scalar select has no named fields or embeddable children ;
		// write_denormalize.go's walkNode relies on that empty result, not an error.
	}
	return fields, nil
}

func (c *sqlCompiler) compileSelectField(node *QueryNode, alias string, f selectField) error {
	switch {
	case f.column != nil:
		if c.format == FormatCBOR && !nativeCBORType(f.column.Type) {
			// no CBOR form for this type : its JSON form, which a jsonb field carries as is
			c.w.Write("to_jsonb(")
			c.qualify(alias, f.column.Name)
			c.w.Write(")")
			return nil
		}
		c.qualify(alias, f.column.Name)
		return nil
	default:
		if child := embedChildOf(f); child != nil {
			return c.compileEmbedField(child, node, alias)
		}
		return c.compileExpr(f.expr, node)
	}
}

// embedChildOf returns the child node f refers to (f.embed, or a bare-alias
// computed field) or nil ; shared with write_denormalize.go's same question.
func embedChildOf(f selectField) *QueryNode {
	if f.embed != nil {
		return f.embed
	}
	if resolved := bareResolvedField(f.expr); resolved != nil {
		if child, ok := resolved.(*QueryNode); ok {
			return child
		}
	}
	return nil
}

// ---- embeds : default (correlated subquery) and LATERAL-shared cases --------------

func (c *sqlCompiler) compileEmbedField(child *QueryNode, parent *QueryNode, parentAlias string) error {
	if plan, shared := c.laterals[child]; shared {
		c.w.Write(plan.alias).Write(".arr")
		return nil
	}

	if child.IsFunction() && !child.Function.ReturnsSet && child.Relation == nil {
		// Scalar function embed (Relation == nil) : contributes directly,
		// no row_to_json/json_agg — see CompileSelect's own doc comment.
		var err error
		c.w.Paren(func() {
			c.w.Write("select ")
			if e := c.compileFunctionCall(child.Function, child.FunctionArguments, child.FunctionArgumentMap, parent); e != nil {
				err = e
				return
			}
		})
		return err
	}

	isToOne := isOutgoingOf(parent, child)
	childAlias := c.allocAlias()

	var innerErr error
	c.w.Paren(func() {
		c.w.Write("select ")
		if isToOne {
			c.w.Write(c.wrapNodeAsValue(child, childAlias))
		} else {
			c.w.Write("coalesce(").Write(c.aggregateRows(c.wrapNodeAsValue(child, childAlias))).Write(", ").Write(c.emptyRows()).Write(")")
		}
		c.w.Write(" from (\n")
		c.w.Indented(func() {
			if err := c.compileNodeCorrelated(child, childAlias, parentAlias); err != nil {
				innerErr = err
			}
		})
		c.w.Write("\n) ").Write(childAlias)
	})
	return innerErr
}

// isOutgoingOf is the canonical to-one check ; a function-rooted embed is
// classified by the same ResolveJoin cardinality as any other node.
func isOutgoingOf(parent *QueryNode, child *QueryNode) bool {
	return slices.Contains(parent.OutgoingNodes, child)
}

// lateralPlan is one IncomingNode's LATERAL-materialization plan, built by
// analyzeLaterals only for a child consumed by >1 output position (step 5).
type lateralPlan struct {
	alias    string
	hasArray bool
	aggs     []*aggConsumer
}

type aggConsumer struct {
	expr  *AggExpr
	alias string
}

// analyzeLaterals counts each incoming child's consumers (embedded array,
// each *AggExpr) ; >1 needs step 5's LEFT JOIN LATERAL exception.
func (c *sqlCompiler) analyzeLaterals(node *QueryNode) (map[*QueryNode]*lateralPlan, error) {
	if len(node.IncomingNodes) == 0 {
		return nil, nil
	}

	plans := map[*QueryNode]*lateralPlan{}
	planFor := func(child *QueryNode) *lateralPlan {
		p := plans[child]
		if p == nil {
			p = &lateralPlan{}
			plans[child] = p
		}
		return p
	}

	if node.Shape != nil {
		for _, field := range node.Shape.Fields {
			qn, ok := field.(*QueryNode)
			if !ok {
				continue
			}
			for _, inc := range node.IncomingNodes {
				if qn == inc {
					planFor(inc).hasArray = true
				}
			}
		}
	}

	walkAggs(node.Select, func(agg *AggExpr) {
		target := aggTargetChild(agg)
		if target == nil {
			return
		}
		for _, inc := range node.IncomingNodes {
			if target == inc {
				p := planFor(inc)
				p.aggs = append(p.aggs, &aggConsumer{expr: agg})
			}
		}
	})

	result := map[*QueryNode]*lateralPlan{}
	for child, p := range plans {
		consumption := len(p.aggs)
		if p.hasArray {
			consumption++
		}
		if consumption <= 1 {
			continue
		}
		p.alias = c.allocAlias()
		for i, a := range p.aggs {
			a.alias = fmt.Sprintf("agg%d", i+1)
		}
		result[child] = p
	}
	return result, nil
}

func (c *sqlCompiler) compileLateralJoin(child *QueryNode, parentAlias string, plan *lateralPlan) error {
	childAlias := c.allocAlias()
	// Registered before compiling the aggs below : each agg's own
	// arguments need child's alias already resolvable.
	c.alias[child] = childAlias

	c.w.Write(" left join lateral (\n")
	var err error
	c.w.Indented(func() {
		c.w.Write("select ")
		first := true
		if plan.hasArray {
			c.w.Write(c.aggregateRows(c.wrapNodeAsValue(child, childAlias))).Write(" as arr")
			first = false
		}
		for _, a := range plan.aggs {
			if !first {
				c.w.Write(", ")
			}
			first = false
			// compileAggFunctionCall, not compileExpr/compileAgg : this
			// emits the aggregate's own definition (see compileAgg's doc comment).
			if e := c.compileAggFunctionCall(a.expr, child, childAlias); e != nil {
				err = e
				return
			}
			c.w.Write(" as ").Write(a.alias)
		}
		c.w.Write(" from (\n")
		c.w.Indented(func() {
			if e := c.compileNodeCorrelated(child, childAlias, parentAlias); e != nil {
				err = e
			}
		})
		c.w.Write("\n) ").Write(childAlias)
	})
	if err != nil {
		return err
	}
	c.w.Write("\n) ").Write(plan.alias).Write(" on true")
	return nil
}

// walkAggs recurses through e's computed sub-expressions, calling fn for
// every *AggExpr found ; only ever called on node.Select, never where/order_by.
func walkAggs(e Expression, fn func(*AggExpr)) {
	switch v := e.(type) {
	case nil:
		return
	case *AggExpr:
		fn(v)
		for _, a := range v.Arguments {
			walkAggs(a, fn)
		}
		walkAggs(v.Filter, fn)
	case *CallExpr:
		for _, a := range v.Arguments {
			walkAggs(a, fn)
		}
	case FoldedExpr:
		walkAggs(v.Left, fn)
		walkAggs(v.Right, fn)
	case BinaryExpr:
		walkAggs(v.Left, fn)
		walkAggs(v.Right, fn)
	case UnaryExpr:
		walkAggs(v.Expr, fn)
	case BetweenExpr:
		walkAggs(v.Min, fn)
		walkAggs(v.Exp, fn)
		walkAggs(v.Max, fn)
	case InExpr:
		walkAggs(v.Subject, fn)
		for _, cand := range v.Candidates {
			walkAggs(cand, fn)
		}
	case AnyAllExpr:
		walkAggs(v.Subject, fn)
		walkAggs(v.Array, fn)
	case ConcatWsExpr:
		walkAggs(v.Separator, fn)
		for _, a := range v.Args {
			walkAggs(a, fn)
		}
	case CoalesceExpr:
		for _, a := range v.Args {
			walkAggs(a, fn)
		}
	case FormatExpr:
		for _, a := range v.Args {
			walkAggs(a, fn)
		}
	case ObjectExpr:
		for _, val := range v.Fields {
			walkAggs(val, fn)
		}
	case StarExpr:
		for _, val := range v.And {
			walkAggs(val, fn)
		}
	case ArrExpr:
		for _, i := range v.Items {
			walkAggs(i, fn)
		}
	case LstExpr:
		for _, i := range v.Items {
			walkAggs(i, fn)
		}
	case IndexExpr:
		walkAggs(v.Array, fn)
		walkAggs(v.Index, fn)
	case SliceExpr:
		walkAggs(v.Array, fn)
		walkAggs(v.From, fn)
		walkAggs(v.To, fn)
	case *ColExpr:
		walkAggs(v.DefaultGet, fn)
		walkAggs(v.DefaultSet, fn)
	case *GetExpr:
		walkAggs(v.DefaultValue, fn)
	case *SetExpr:
		walkAggs(v.DefaultValue, fn)
	default:
		// Identifier, literals, StringLiteral, ParamExpr, DefaultKeyword :
		// no computed subtree, nothing to recurse into.
	}
}

// aggTargetChild finds the *QueryNode agg's arguments target (a "." chain's
// root, or a bare Identifier) ; scans every argument, arity varies by function.
func aggTargetChild(agg *AggExpr) *QueryNode {
	for _, arg := range agg.Arguments {
		if qn := childReferencedBy(arg); qn != nil {
			return qn
		}
	}
	return nil
}

// childReferencedBy finds the first relation a "." chain inside e starts from,
// looking through operators and casts : sum(amount::numeric) and
// string_agg(amount::text, ', ') still aggregate over amount's relation.
func childReferencedBy(e Expression) *QueryNode {
	if qn := rootQueryNodeOf(e); qn != nil {
		return qn
	}
	first := func(es ...Expression) *QueryNode {
		for _, x := range es {
			if qn := childReferencedBy(x); qn != nil {
				return qn
			}
		}
		return nil
	}
	switch v := e.(type) {
	case UnaryExpr:
		return first(v.Expr)
	case BinaryExpr:
		return first(v.Left, v.Right)
	case FoldedExpr:
		return first(v.Left, v.Right)
	case BetweenExpr:
		return first(v.Min, v.Exp, v.Max)
	case InExpr:
		return first(append([]Expression{v.Subject}, v.Candidates...)...)
	case CoalesceExpr:
		return first(v.Args...)
	case *CallExpr:
		return first(v.Arguments...)
	}
	return nil
}

func rootQueryNodeOf(e Expression) *QueryNode {
	if resolved := bareResolvedField(e); resolved != nil {
		qn, _ := resolved.(*QueryNode)
		return qn
	}
	if v, ok := e.(FoldedExpr); ok && v.Op == FoldDot {
		return rootQueryNodeOf(v.Left)
	}
	return nil
}

// bareResolvedField returns e's Resolved field when e is a bare reference —
// *Identifier (a "." reference/hop name) or *ColExpr (["col", ...], a real
// physical column) — nil otherwise.
func bareResolvedField(e Expression) ResolvedField {
	switch v := e.(type) {
	case *Identifier:
		return v.Resolved
	case *ColExpr:
		return v.Resolved
	default:
		return nil
	}
}
