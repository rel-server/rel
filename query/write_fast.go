package query

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/writer"
	"github.com/samber/oops"
)

// The fast write path : for the common case of a flat write (one relation, no
// nested writes, a select of plain columns), skip the general temp table. The
// request's rows go in as one binary array parameter of the table's fast row type,
// the write statement returns the written rows (RETURNING), and the response is
// built from them in the same statement. See write.go for where it is chosen.

// fastWrite is the state of one fast write.
type fastWrite struct {
	// param is the rows (or, for an "update" root, the one patch row) as an array of the fast
	// row type, and patchMask the presence words of the first row.
	param     rawBinary
	patchMask []int64
	// responses is filled by finishFast : one JSON document per response row.
	responses [][]byte
}

// fastWriteEligible reports whether root can be written without the general temp table. It is
// deliberately narrow : anything it does not recognize takes the general path.
func fastWriteEligible(root *QueryNode, opts WriteOptions) bool {
	if opts.NoFastPath || opts.Stats || opts.Sql || opts.QueryPlan {
		return false
	}
	switch root.WriteMode {
	case INSERT, UPSERT, UPDATE_EXISTING, UPDATE:
	default:
		return false
	}
	if root.Relation == nil || root.IsFunction() || root.Parent != nil ||
		len(root.OutgoingNodes) > 0 || len(root.IncomingNodes) > 0 {
		return false
	}
	if !isShapeProducingSelect(root.Select) {
		return false
	}
	// The response reads columns off the written rows, so every output must be a
	// plain expression over the relation's own columns : no computed field, join
	// or aggregate, which need the table's own row type.
	fields, err := selectFieldsFor(root)
	if err != nil {
		return false
	}
	for _, f := range fields {
		if f.embed != nil {
			return false
		}
		if f.column == nil && !plainRowExpr(f.expr, root) {
			return false
		}
	}
	if !plainRowExpr(root.Where, root) {
		return false
	}
	for _, t := range root.OrderBy {
		if !plainRowExpr(t.Expr, root) {
			return false
		}
	}
	for _, e := range root.DistinctOn {
		if !plainRowExpr(e, root) {
			return false
		}
	}
	return true
}

// plainRowExpr reports whether e only combines literals, params and root's own
// plain columns with operators.
func plainRowExpr(e Expression, root *QueryNode) bool {
	switch v := e.(type) {
	case nil, NullLiteral, BoolLiteral, NumberLiteral, StringLiteral, BigIntLiteral, NumericLiteral, ParamExpr:
		return true
	case *Identifier:
		cp, ok := v.Resolved.(ColumnPath)
		return ok && cp.Node == root && len(cp.Path) == 1
	case *ColExpr:
		return v.DefaultGet == nil && v.DefaultSet == nil && v.ResolvedColumn != nil
	case UnaryExpr:
		return plainRowExpr(v.Expr, root)
	case BinaryExpr:
		return plainRowExpr(v.Left, root) && plainRowExpr(v.Right, root)
	case FoldedExpr:
		return v.Op != FoldDot && plainRowExpr(v.Left, root) && plainRowExpr(v.Right, root)
	case BetweenExpr:
		return plainRowExpr(v.Min, root) && plainRowExpr(v.Exp, root) && plainRowExpr(v.Max, root)
	case InExpr:
		if !plainRowExpr(v.Subject, root) {
			return false
		}
		for _, c := range v.Candidates {
			if !plainRowExpr(c, root) {
				return false
			}
		}
		return true
	}
	return false
}

// writeWith opens the statement's WITH list ; in fast mode with the "tmp" CTE
// that stands in for the node's temp rows, built from the binary array parameter.
func (dc *dmlCompiler) writeWith(w *writer.SQLWriter, node *QueryNode) error {
	w.Write("with ")
	if dc.fast == nil {
		return nil
	}
	t := dc.tempOf(node)
	w.Write("tmp as (select z.*, z.ordinality::int as ")
	w.Write(quoteIdent(t.rowIDCol()))
	w.Write(", ")
	w.Write(fmt.Sprintf("%d", dc.ids[node]))
	w.Write(" as ")
	w.Write(quoteIdent(t.nodeIDCol()))
	w.Write(", null::int as ")
	w.Write(quoteIdent(t.parentIDCol()))
	w.Write(" from unnest(")
	w.Bind(dc.fast.param)
	w.Write("::")
	w.Write(t.fast.qualified())
	w.Write("[]) with ordinality as z),\n")
	return nil
}

// finishFast ends a fast statement : the response select over cte (the CTE that
// RETURNs the written rows), then runs it and keeps the response rows.
func (dc *dmlCompiler) finishFast(ctx context.Context, node *QueryNode, w *writer.SQLWriter, cte string) error {
	c := &sqlCompiler{w: w, alias: map[*QueryNode]string{}, fromCTE: cte, orderByRowID: node.WriteMode == UPDATE_EXISTING, format: dc.format}
	alias := c.allocAlias()
	w.Write("select ").Write(c.wrapNodeAsValue(node, alias)).Write(" from (\n")
	w.Indent()
	err := c.compileNode(node, alias)
	w.Unindent()
	if err != nil {
		return err
	}
	w.Write("\n) ").Write(alias)

	args, err := dc.args(w)
	if err != nil {
		return err
	}
	if dc.format == FormatCBOR { // the rows are records in binary
		args = append([]any{pgx.QueryResultFormats{pgx.BinaryFormatCode}}, args...)
	}
	rows, err := dc.conn.Query(ctx, w.String(), args...)
	if err != nil {
		return oops.With("sql", w.String()).Wrapf(err, "write")
	}
	defer rows.Close()
	dc.fast.responses = [][]byte{}
	for rows.Next() {
		// the raw value, not a Scan : a binary record has no scan target but its own codec's
		dc.fast.responses = append(dc.fast.responses, append([]byte{}, rows.RawValues()[0]...))
	}
	if err := rows.Err(); err != nil {
		return oops.With("sql", w.String()).Wrapf(err, "write")
	}
	return nil
}

// runUpdateWhereFast is runUpdateWhere without the general temp table : the patch comes in
// as a one-row array parameter and the updated rows are the response.
func (dc *dmlCompiler) runUpdateWhereFast(ctx context.Context, node *QueryNode) error {
	setCols, err := patchSetColumns(node, dc.fast.patchMask)
	if err != nil {
		return err
	}
	t := dc.tempOf(node)
	relID := node.Relation.Identifier.EscapedString()
	w := writer.NewSQL()
	w.Write("with p as (select z.* from unnest(")
	w.Bind(dc.fast.param)
	w.Write("::")
	w.Write(t.fast.qualified())
	w.Write("[]) as z),\nupd as (\n\tupdate ")
	w.Write(relID)
	w.Write(" t\n\tset ")
	for i, cp := range setCols {
		if i > 0 {
			w.Write(", ")
		}
		writeTargetPath(w, cp)
		w.Write(" = ")
		w.Write(t.cpRef("p", cp.Path))
	}
	w.Write("\n\tfrom p\n\twhere ")
	if err := dc.compileRootWhere(w, node); err != nil {
		return err
	}
	w.Write("\n\treturning t.*\n)\n")
	return dc.finishFast(ctx, node, w, "upd")
}

// executeFast runs the whole write as one statement and returns the response rows.
func executeFast(ctx context.Context, conn Querier, root *QueryNode, ids map[*QueryNode]int, payload Node, state *WriteState, nextNodeID int, paramValues map[string]any, ec *encodeContext, format OutputFormat) (*WriteResult, error) {
	t, err := ensureTempTables(ctx, conn, root.Relation)
	if err != nil {
		return nil, err
	}
	tables := map[*pg.Relation]*tempTable{root.Relation: t}
	if payload == nil {
		return nil, oops.Errorf("write: payload must be an array of root rows")
	}
	rows, err := denormalizeNode(root, ids, payload, tables, 0, false, ec)
	if err != nil {
		return nil, err
	}
	if rows.count == 0 {
		state.nextNodeID = nextNodeID
		return &WriteResult{NodeIDs: ids, Rows: [][]byte{}}, nil
	}
	dc := &dmlCompiler{conn: conn, ids: ids, temps: tables, paramValues: paramValues, format: format,
		fast: &fastWrite{param: rows.batches[t].param(), patchMask: rows.patchMask}}
	if err := dc.runPhase1Node(ctx, root); err != nil {
		return nil, oops.With("node", root.InnerName).Wrapf(err, "write")
	}
	state.nextNodeID = nextNodeID
	return &WriteResult{NodeIDs: ids, Rows: dc.fast.responses}, nil
}
