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

	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/writer"
)

// sqlCompiler carries state shared across one whole CompileSelect call : the
// output writer, a monotonic alias counter (every subquery/table/LATERAL
// alias is "t1", "t2", ... in visitation order — simplest scheme, always
// unique regardless of nesting depth, no meaning attached to the number),
// and a Node -> its-current-alias map so a "." chain or agg argument that
// resolved against a DIFFERENT node than the one currently being compiled
// (e.g. a LATERAL-sourced agg's own argument, resolved against the child's
// alias) still finds the right qualifier.
type sqlCompiler struct {
	w         *writer.SQLWriter
	nextAlias int
	alias     map[*QueryNode]string

	// laterals holds the current node's own LATERAL-sharing plan (nil
	// outside of compileNode's own select-list emission) — see
	// analyzeLaterals. Saved/restored around each recursive compileNode
	// call so nested nodes don't see their parent's plan.
	laterals map[*QueryNode]*lateralPlan

	// dataScopeRoot/dataScopeNodeID/dataScopeAlias are set only by
	// CompileSelectForDataNode, and checked only when compiling that exact
	// node (never a descendant) — see its own doc comment.
	dataScopeRoot   *QueryNode
	dataScopeNodeID int
	dataScopeAlias  string
}

func newSQLCompiler() *sqlCompiler {
	return &sqlCompiler{w: writer.NewSQL(), alias: map[*QueryNode]string{}}
}

func (c *sqlCompiler) allocAlias() string {
	c.nextAlias++
	return fmt.Sprintf("t%d", c.nextAlias)
}

// qualify writes "alias.name" (escaped) — a plain qualified column
// reference. A small helper purely to avoid SQLWriter's chaining caveat
// (see writer/pg.go's SQLWriter doc comment) : c.w.Write(alias).Write(".")
// returns the embedded *Writer, which has no .Id, so this pattern needs
// separate statements on c.w rather than one chain — easy to get wrong at
// each call site, so it's centralized here instead.
func (c *sqlCompiler) qualify(alias, name string) {
	c.w.Write(alias)
	c.w.Write(".")
	c.w.Id(name)
}

// CompileSelect compiles root into one executable SQL statement returning
// one row per root row, each with a single "json" column produced by
// row_to_json — exactly parallel to how a to-one embed is wrapped (see
// compileEmbedField), so the root needs no special-casing there. A
// genuinely scalar (no Relation at all — a bare, non-composite return
// type) function root is the one real exception, per Reading Algorithm
// step 6 : its result contributes directly, with no row_to_json wrapping
// at all (specs/query-engine.md ## Response Shape : "the scalar of the
// result of a scalar function"). A function root that returns a single
// composite ROW (not SETOF, but still a real, indexed relation type —
// Relation != nil) is NOT this case : it goes through the ordinary
// compileNode path below like any other node, since compileFrom already
// handles a function call as a FROM-clause source generically (Postgres
// allows a function call anywhere a table can go, regardless of whether
// it's set-returning) — the exact same machinery a SETOF function root
// already uses, just naturally producing one row instead of many. Gating
// on Function.ReturnsSet alone (without also checking Relation == nil)
// used to route both cases through the bare-scalar shortcut, silently
// dropping any join/select/where a single-row composite function root
// declared, and skipping row_to_json entirely — which produced Postgres's
// raw composite-literal text ("(1,name,...)"), not valid JSON, breaking
// the manual response streaming even with no join at all.
//
// The caller owns executing the statement and manually streaming the
// response's "["/","/"]" per ## Response Shape — this function's contract
// stops at "here is one valid, executable statement plus its bind args".
func CompileSelect(root *QueryNode) (*writer.SQLWriter, error) {
	c := newSQLCompiler()

	if root.IsFunction() && !root.Function.ReturnsSet && root.Relation == nil {
		c.w.Write("select ")
		if err := c.compileFunctionCall(root.Function, root.FunctionArguments, root.FunctionArgumentMap, root.Parent); err != nil {
			return nil, err
		}
		return c.w, nil
	}

	alias := c.allocAlias()
	c.w.Write("select ").Write(wrapNodeAsValue(root, alias)).Write(" from (\n")
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
// touched, via "_data" — nodeID is root's own assigned __node_id (see
// ExecuteWrite/WriteResult.NodeIDs), and only its root-level rows
// (__parent_id is null) are in scope. Used for the write-then-reread
// response (specs/query-engine.md ## Response Shape : the response is built by
// a separate, read-only statement issued after the write transaction's
// commit, never from inside it) — "_data" must already exist and hold this
// request's rows on the connection this statement later runs against.
//
// A function-rooted node is rejected : functions aren't writable
// (query/node.go), so there's never a "_data" row for one to scope against.
func CompileSelectForDataNode(root *QueryNode, nodeID int) (*writer.SQLWriter, error) {
	if root.IsFunction() {
		return nil, fmt.Errorf("sql: a function-rooted node can't be write-scoped — functions aren't writable")
	}

	c := newSQLCompiler()
	c.dataScopeRoot = root
	c.dataScopeNodeID = nodeID
	c.dataScopeAlias = "__wq"

	alias := c.allocAlias()
	c.w.Write("select ").Write(wrapNodeAsValue(root, alias)).Write(" from (\n")
	c.w.Indent()
	err := c.compileNode(root, alias)
	c.w.Unindent()
	if err != nil {
		return nil, err
	}
	c.w.Write("\n) ").Write(alias)
	return c.w, nil
}

// compileNode emits a PLAIN multi-column SELECT for node — no
// row_to_json/json_agg wrapper, so the exact same function serves the root
// (wrapped by CompileSelect) and every embed (wrapped by
// compileEmbedField) uniformly :
//
//	select <select-list> from <relation-or-function> <alias> [left join lateral (...) ...]
//	where <on-clause AND node's own where>
//	order by <node's own order_by>
//	limit <node's own limit> offset <node's own offset>
//
// onParentAlias/onNode are "" when node is the root (no correlation needed)
// — otherwise the caller (compileEmbedField) supplies the parent's alias so
// the on-clause (node.JoinColumns) can be folded into this node's own where.
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
			return fmt.Errorf("sql: node has no select fields to emit")
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
		// A scalar select — query-engine.md ## Reading Algorithm ###
		// Scalar-selected nodes : one to_jsonb(...)-cast value per row,
		// named "__scalar" (an internal name, never visible outside the
		// SQL this package generates — matching __row_id/__node_id/
		// __parent_id's own convention). to_jsonb, not a bare emission :
		// server/response.go's streamRows writes each row's single column
		// straight through as the response bytes, so it must already be
		// valid JSON regardless of the expression's own Postgres type —
		// unquoted text or a raw composite value isn't.
		c.w.Write("to_jsonb(")
		if err := c.compileExpr(node.Select, node); err != nil {
			return err
		}
		c.w.Write(") as __scalar")
	}

	c.w.Write(" from ")
	if err := c.compileFrom(node, alias); err != nil {
		return err
	}

	for _, child := range node.IncomingNodes {
		plan, shared := laterals[child]
		if !shared {
			continue
		}
		if err := c.compileLateralJoin(child, alias, plan); err != nil {
			return err
		}
	}

	if err := c.compileWhere(node, alias, onParentAlias); err != nil {
		return err
	}
	if err := c.compileOrderBy(node); err != nil {
		return err
	}
	c.compileLimitOffset(node)

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
		return fmt.Errorf("sql: node has no relation to select from")
	}
	c.w.Write(node.Relation.Identifier.EscapedString()).Write(" ").Write(alias)
	if c.dataScopeRoot != nil && node == c.dataScopeRoot {
		c.w.Write(" inner join _data ")
		c.w.Write(c.dataScopeAlias)
		c.w.Write(" on ")
		c.w.Write(c.dataScopeAlias)
		c.w.Write(".__node_id = ")
		c.w.Write(fmt.Sprintf("%d", c.dataScopeNodeID))
		c.w.Write(" and ")
		c.w.Write(c.dataScopeAlias)
		c.w.Write(".__parent_id is null")
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
			return fmt.Errorf("sql: %q has no identity (on_conflict or primary key) column set to scope the reread by", node.InnerName)
		}
		for i, col := range cols {
			if i > 0 {
				c.w.Write(" and ")
			}
			c.qualify(alias, col.Name)
			c.w.Write(" = (")
			c.w.Write(c.dataScopeAlias)
			c.w.Write(".keys->>")
			c.w.Bind(col.Name)
			c.w.Write("::text)::")
			c.w.Write(col.Type.PgIdentifier.EscapedString())
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
			// Default to payload order when the write query specified no
			// order_by of its own — an explicit one is a read-shape choice
			// independent of write scoping, so it's left untouched below.
			c.w.Write(" order by ")
			c.w.Write(c.dataScopeAlias)
			c.w.Write(".__row_id")
		}
		return nil
	}
	c.w.Write(" order by ")
	for i, term := range node.OrderBy {
		if i > 0 {
			c.w.Write(", ")
		}
		if err := c.compileExpr(term.Expr, node); err != nil {
			return err
		}
		switch term.Direction {
		case OrderDesc:
			// query.ts : "asc and desc are nulls last by default" — bare
			// "desc" is NOT that on its own : Postgres's own default for
			// DESC is NULLS FIRST (only plain ASC defaults to NULLS LAST),
			// verified directly against Postgres 16. Must say so
			// explicitly, or a bare `["desc", "col"]` order_by term
			// silently sorts nulls opposite to what the spec promises.
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

// selectField is one entry of a node's own select-list — either a plain
// physical column (own/full's base), a child alias (full's embed), or a
// computed expression (an "and"/object-literal field, or a bare
// get/get-set select). Deliberately separate from pass 2's Shape.Fields
// (query/resolved_field.go) : Shape only records what a name RESOLVES TO
// for ".", chaining purposes (often nil/opaque for anything not itself
// chainable, e.g. an agg result) — codegen needs the actual Expression/
// column to emit, which Shape doesn't carry.
type selectField struct {
	key    string
	column *pg.Column // set for a plain own/full column
	embed  *QueryNode // set for a full-included child alias
	expr   Expression // set for a computed field
}

// isShapeProducingSelect reports whether sel is one of the constructs
// selectFieldsFor knows how to expand into a named-field list — own/full
// and their -except/-and variants, an inline object literal, or a bare
// get/get-set. Anything else (a bare column/alias, an arithmetic
// expression, a function call, "arr"/"lst", a bare "agg", ...) is a SCALAR
// select instead : compileNodeCorrelated branches on this to decide
// between the ordinary named-field select list and the single to_jsonb(...)
// "__scalar" column ## Response Shape's "distinct shape... for a scalar
// function" gets uniformly for a table-rooted node (or embed) too — see
// query-engine.md ## Reading Algorithm ### Scalar-selected nodes.
func isShapeProducingSelect(sel Expression) bool {
	switch sel.(type) {
	case OwnExpr, FullExpr, OwnExceptExpr, FullExceptExpr, OwnAndExpr, FullAndExpr, OwnExceptAndExpr, FullExceptAndExpr, ObjectExpr, *GetSetExpr, *GetExpr:
		return true
	default:
		return false
	}
}

// wrapNodeAsValue writes node's own already-compiled inner query (aliased
// as innerAlias) as ONE jsonb value : row_to_json(innerAlias) for an
// ordinary shape-producing select, or innerAlias's own single "__scalar"
// column directly for a scalar one — compileNodeCorrelated's two branches,
// query-engine.md ## Reading Algorithm ### Scalar-selected nodes. Shared
// by every place that turns one already-compiled node into a value :
// CompileSelect/CompileSelectForDataNode's own root wrapping,
// compileEmbedField's to-one/to-many wrapping, and compileLateralJoin's
// own json_agg(...) array materialization.
func wrapNodeAsValue(node *QueryNode, innerAlias string) string {
	if isShapeProducingSelect(node.Select) {
		return "row_to_json(" + innerAlias + ")"
	}
	return innerAlias + ".__scalar"
}

// selectFieldsFor's own switch enumerates the identical type set
// isShapeProducingSelect (above) checks — Go's type-switch dispatch can't
// cheaply share a case list across two functions with different jobs
// (classify vs. actually expand each case), so this is two independently-
// written enumerations of the same 11 types. Kept deliberately adjacent in
// this file so a future case added to query.ts's shape-producing family is
// visibly added to both at once ; if either one drifts, isShapeProducingSelect
// would call something "scalar" that this function still knows how to
// expand (or vice versa), which wrapNodeAsValue would then wrap wrong.
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
	case OwnExpr:
		addOwn(nil)
	case FullExpr:
		addOwn(nil)
		addAliases()
	case OwnExceptExpr:
		addOwn(v.Except)
	case FullExceptExpr:
		addOwn(v.Except)
		addAliases()
	case OwnAndExpr:
		addOwn(nil)
		addAnd(v.And)
	case FullAndExpr:
		addOwn(nil)
		addAliases()
		addAnd(v.And)
	case OwnExceptAndExpr:
		addOwn(v.Except)
		addAnd(v.And)
	case FullExceptAndExpr:
		addOwn(v.Except)
		addAliases()
		addAnd(v.And)
	case ObjectExpr:
		addAnd(v.Fields)
	case *GetSetExpr:
		fields = append(fields, selectField{key: v.ResolvedColumn.Name, expr: v})
	case *GetExpr:
		fields = append(fields, selectField{key: v.ResolvedColumn.Name, expr: v})
	default:
		// A scalar select (isShapeProducingSelect is false) has no named
		// fields, and in particular no embeddable children reachable
		// through top-level field iteration — write_denormalize.go's
		// walkNode calls this unconditionally to discover embed children
		// to walk into, and correctly finds none here, not an error : a
		// "." hop inside a scalar expression reads a value, it doesn't
		// expect a nested JSON payload shape the way a real embed does.
		// compileNodeCorrelated (below) never reaches this branch at all —
		// it checks isShapeProducingSelect itself and takes the
		// to_jsonb(...) "__scalar" path instead of calling this function.
	}
	return fields, nil
}

func (c *sqlCompiler) compileSelectField(node *QueryNode, alias string, f selectField) error {
	switch {
	case f.column != nil:
		c.qualify(alias, f.column.Name)
		return nil
	default:
		if child := embedChildOf(f); child != nil {
			return c.compileEmbedField(child, node, alias)
		}
		return c.compileExpr(f.expr, node)
	}
}

// embedChildOf returns the child node f actually refers to — either
// selectFieldsFor's own/full alias inclusion (f.embed), or an explicit
// computed field (an "and"/object-literal entry) that's itself a bare alias
// reference to a child (e.g. {"movies": "movies"}), resolved by pass 2 to
// the same *QueryNode landing "full"'s implicit inclusion produces, just
// reached through an ordinary Identifier instead. nil if f is a plain
// column/computed value, not a child embed at all. Shared between the read
// path (compileSelectField) and the write path's denormalizer
// (write_denormalize.go), which both need the same answer to "does this
// select key point at a child node".
func embedChildOf(f selectField) *QueryNode {
	if f.embed != nil {
		return f.embed
	}
	if id, ok := f.expr.(*Identifier); ok {
		if child, ok := id.Resolved.(*QueryNode); ok {
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
		// A genuinely scalar function embed (no Relation — a bare,
		// non-composite return type) contributes its result directly
		// (Reading Algorithm step 6), no row_to_json/json_agg wrapping. A
		// single-row composite function embed (Relation != nil) is NOT
		// this case — see CompileSelect's own doc comment for the full
		// reasoning ; it falls through below to the ordinary to-one/
		// to-many wrapping path instead, same as it would if declared as
		// a plain relation join.
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
			c.w.Write(wrapNodeAsValue(child, childAlias))
		} else {
			c.w.Write("coalesce(json_agg(").Write(wrapNodeAsValue(child, childAlias)).Write("), '[]'::json)")
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

// isOutgoingOf reports whether child is one of parent's OutgoingNodes (a
// to-one embed) — used instead of trusting IsFunction/Relation-shape alone,
// since a function-rooted embed is also classified outgoing/incoming by the
// same ResolveJoin cardinality as any other node (node_resolve.go). The
// canonical way to ask this question anywhere in this package — reuse it
// rather than a fresh slices.Contains(parent.OutgoingNodes, child), which
// is exactly this line with no name attached ; isIncoming
// (write_denormalize.go) is its to-many counterpart.
func isOutgoingOf(parent *QueryNode, child *QueryNode) bool {
	return slices.Contains(parent.OutgoingNodes, child)
}

// lateralPlan is one IncomingNode's LATERAL-materialization plan — built by
// analyzeLaterals, only for children whose rows are consumed by more than
// one output position in the parent's own select (Reading Algorithm step
// 5's exception).
type lateralPlan struct {
	alias    string
	hasArray bool
	aggs     []*aggConsumer
}

type aggConsumer struct {
	expr  *AggExpr
	alias string
}

// analyzeLaterals counts, per incoming child, how many output positions in
// node's own select consume its rows — an embedded array (+1, detected via
// the already-computed Shape.Fields : a *QueryNode landing among
// IncomingNodes) and each distinct *AggExpr targeting that child (+1 each,
// detected by walking node.Select's computed sub-expressions and tracing
// each agg's own arguments back to a "." chain rooted at that child's
// alias). Consumption > 1 is exactly the case the Reading Algorithm's LEFT
// JOIN LATERAL exception exists for : a SELECT-list subquery can only ever
// be reused for ONE output column, so two consumers of the same child would
// otherwise mean compiling (and re-executing) that child's own query twice
// — wasteful, and with a non-total order_by/limit, not even guaranteed to
// agree with itself.
//
// This walk only ever considers IncomingNodes : query.ts's own note on
// "agg" is that its target "must be an incoming relation," so an
// OutgoingNode (a to-one embed) is never a LATERAL candidate at all.
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
	// Registered before compiling the aggs below (not just inside the
	// recursive compileNodeCorrelated call at the bottom) : each agg's own
	// arguments need child's alias already resolvable — see
	// compileAggFunctionCall — and that must happen before compileExpr ever
	// looks it up, not after.
	c.alias[child] = childAlias

	c.w.Write(" left join lateral (\n")
	var err error
	c.w.Indented(func() {
		c.w.Write("select ")
		first := true
		if plan.hasArray {
			c.w.Write("json_agg(").Write(wrapNodeAsValue(child, childAlias)).Write(") as arr")
			first = false
		}
		for _, a := range plan.aggs {
			if !first {
				c.w.Write(", ")
			}
			first = false
			// compileAggFunctionCall directly, NOT compileExpr/compileAgg :
			// this emits the aggregate's own DEFINITION, and compileAgg's
			// shared-vs-not lookup (reached via compileExpr) would instead
			// try to reference this same, not-yet-defined lateral column —
			// see compileAgg's doc comment.
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
// every *AggExpr found. Scoped to the shapes that can actually appear
// inside a node's own select (own/full's base columns and child aliases
// have no computed subtree at all, so aren't cases here) — where/order_by
// never contain a meaningful "agg" (aggregating a child's rows only makes
// sense as part of shaping THIS node's own output), so this is only ever
// called on node.Select.
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
			if !cand.IsLiteral {
				walkAggs(cand.Expr, fn)
			}
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
	case OwnAndExpr:
		for _, val := range v.And {
			walkAggs(val, fn)
		}
	case FullAndExpr:
		for _, val := range v.And {
			walkAggs(val, fn)
		}
	case OwnExceptAndExpr:
		for _, val := range v.And {
			walkAggs(val, fn)
		}
	case FullExceptAndExpr:
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
	case *GetSetExpr:
		walkAggs(v.DefaultGet, fn)
		walkAggs(v.DefaultSet, fn)
	case *GetExpr:
		walkAggs(v.DefaultValue, fn)
	case *SetExpr:
		walkAggs(v.DefaultValue, fn)
	default:
		// Identifier, literals, Own/Full/Except (no computed subtree of
		// their own), Star, ParamExpr, DefaultKeyword : nothing to recurse
		// into.
	}
}

// aggTargetChild finds the *QueryNode agg's arguments actually target — the
// root Identifier of a "." chain (or a bare Identifier alone, e.g.
// count(*)-style "just this relation's rows") landing on a *QueryNode.
// Scans every argument, not just the first, since which position carries
// the relation reference depends on the aggregate function's own arity.
func aggTargetChild(agg *AggExpr) *QueryNode {
	for _, arg := range agg.Arguments {
		if qn := rootQueryNodeOf(arg); qn != nil {
			return qn
		}
	}
	return nil
}

func rootQueryNodeOf(e Expression) *QueryNode {
	switch v := e.(type) {
	case *Identifier:
		qn, _ := v.Resolved.(*QueryNode)
		return qn
	case FoldedExpr:
		if v.Op == FoldDot {
			return rootQueryNodeOf(v.Left)
		}
	}
	return nil
}
