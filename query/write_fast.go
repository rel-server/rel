package query

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/rel-server/rel/writer"
	"github.com/samber/oops"
)

// The fast write path : for the common case of a flat write (one relation, no
// nested writes, a select of plain columns), skip the "_data" scratch table. The
// request's rows go in as one JSON parameter, the write statement returns the
// written rows (RETURNING), and the response is built from them in the same
// statement. See write.go for where it is chosen.

// fastWrite is the state of one fast write.
type fastWrite struct {
	// rows is the JSON array of flat row objects (insert, upsert, update-existing),
	// or patch the single patch object (update).
	rows  []byte
	patch []byte
	// responses is filled by finishFast : one JSON document per response row.
	responses [][]byte
}

// fastWriteEligible reports whether root can be written without "_data". It is
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
// that stands in for the node's "_data" rows, built from the JSON parameter.
func (dc *dmlCompiler) writeWith(w *writer.SQLWriter, node *QueryNode) error {
	w.Write("with ")
	if dc.fast == nil {
		return nil
	}
	w.Write("tmp as (select o.ord::int as __row_id, ")
	w.Write(fmt.Sprintf("%d", dc.ids[node]))
	w.Write(" as __node_id, null::int as __parent_id, o.data from jsonb_array_elements(")
	w.Bind(string(dc.fast.rows))
	w.Write("::jsonb) with ordinality as o(data, ord)),\n")
	return nil
}

// finishFast ends a fast statement : the response select over cte (the CTE that
// RETURNs the written rows), then runs it and keeps the response rows.
func (dc *dmlCompiler) finishFast(ctx context.Context, node *QueryNode, w *writer.SQLWriter, cte string) error {
	c := &sqlCompiler{w: w, alias: map[*QueryNode]string{}, fromCTE: cte, orderByRowID: node.WriteMode == UPDATE_EXISTING}
	alias := c.allocAlias()
	w.Write("select ").Write(wrapNodeAsValue(node, alias)).Write(" from (\n")
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
	rows, err := dc.conn.Query(ctx, w.String(), args...)
	if err != nil {
		return oops.With("sql", w.String()).Wrapf(err, "write")
	}
	defer rows.Close()
	dc.fast.responses = [][]byte{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return oops.Wrapf(err, "write: reading the written rows")
		}
		dc.fast.responses = append(dc.fast.responses, append([]byte(nil), raw...))
	}
	if err := rows.Err(); err != nil {
		return oops.With("sql", w.String()).Wrapf(err, "write")
	}
	return nil
}

// runUpdateWhereFast is runUpdateWhere without "_data" : the patch comes in as
// a JSON parameter and the updated rows are the response.
func (dc *dmlCompiler) runUpdateWhereFast(ctx context.Context, node *QueryNode) error {
	var present map[string]json.RawMessage
	if err := sonic.Unmarshal(dc.fast.patch, &present); err != nil {
		return oops.Wrapf(err, "decoding the patch")
	}
	allowlist := node.UpdateColumns
	if len(allowlist) == 0 {
		allowlist = node.InsertColumns
	}
	cols, err := columnsFor(node, allowlist)
	if err != nil {
		return err
	}
	var setCols []ColumnPath
	for _, cp := range cols {
		if _, ok := present[columnPathFlatName(cp)]; ok {
			setCols = append(setCols, cp)
		}
	}
	if len(setCols) == 0 {
		return oops.Errorf("update: the patch names no column the select makes writable")
	}

	relID := node.Relation.Identifier.EscapedString()
	w := writer.NewSQL()
	w.Write("with p as (select obj.* from jsonb_populate_record(null::")
	w.Write(relID)
	w.Write(", ")
	w.Bind(string(dc.fast.patch))
	w.Write("::jsonb) as obj),\nupd as (\n\tupdate ")
	w.Write(relID)
	w.Write(" t\n\tset ")
	for i, cp := range setCols {
		if i > 0 {
			w.Write(", ")
		}
		writeTargetPath(w, cp)
		w.Write(" = ")
		writeQualifiedPathN(w, "p", cp.Path, len(cp.Path)-1)
	}
	w.Write("\n\tfrom p\n\twhere ")
	if err := dc.compileRootWhere(w, node); err != nil {
		return err
	}
	w.Write("\n\treturning t.*\n)\n")
	return dc.finishFast(ctx, node, w, "upd")
}

// executeFast runs the whole write as one statement and returns the response rows.
func executeFast(ctx context.Context, conn Querier, root *QueryNode, ids map[*QueryNode]int, payload []byte, state *WriteState, nextNodeID int, paramValues map[string]any) (*WriteResult, error) {
	dc := &dmlCompiler{conn: conn, ids: ids, paramValues: paramValues, fast: &fastWrite{}}
	if root.WriteMode == UPDATE {
		dc.fast.patch = append([]byte(nil), payload[1:len(payload)-1]...) // patchAsPayload wrapped it in [ ]
	} else {
		rows, _, err := denormalize(root, ids, payload, 0)
		if err != nil {
			return nil, err
		}
		data := make([]json.RawMessage, len(rows))
		for i, r := range rows {
			data[i] = r.Data
		}
		encoded, err := sonic.Marshal(data)
		if err != nil {
			return nil, oops.Wrapf(err, "write: encoding rows")
		}
		dc.fast.rows = encoded
		if len(rows) == 0 {
			state.nextNodeID = nextNodeID
			return &WriteResult{NodeIDs: ids, Rows: [][]byte{}}, nil
		}
	}
	if err := dc.runPhase1Node(ctx, root); err != nil {
		return nil, oops.With("node", root.InnerName).Wrapf(err, "write")
	}
	state.nextNodeID = nextNodeID
	return &WriteResult{NodeIDs: ids, Rows: dc.fast.responses}, nil
}
