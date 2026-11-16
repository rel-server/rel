// Phase 1 (insert/update/upsert) and phase 2 (delete) DML.
package query

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/writer"
	"github.com/samber/oops"
)

type dmlCompiler struct {
	conn Querier
	ids  map[*QueryNode]int

	// paramValues resolves a well-known write's $param references ; nil for a plain /rel write.
	paramValues map[string]any

	// temps holds the temp table of every relation the request writes.
	temps map[*pg.Relation]*tempTable

	// tableNodes is, per relation, the node ids that have rows in its temp table so far in the request.
	tableNodes map[*pg.Relation]map[int]bool

	// populated holds the node ids that received a row from denormalize.
	populated map[int]bool

	// These back the `stats` response ; submitted is keyed by __node_id. statOrder keeps first-touch order
	// across both phases, since a node touched by both (MERGE's upsert then delete) has a single Stat.
	collectStats bool
	submitted    map[int]int
	statsByNode  map[*QueryNode]*Stat
	statOrder    []*QueryNode

	// These back the `sql` response : each DML statement's text, collected as it runs.
	collectSQL bool
	sqlByNode  map[*QueryNode]*SqlResult
	sqlOrder   []*QueryNode

	// These back the `query_plan` response : each DML statement runs for real under EXPLAIN (ANALYZE, FORMAT JSON).
	// Never set with collectStats ; relexec rejects that combination.
	explainAnalyze bool
	planByNode     map[*QueryNode]*PlanResult
	planOrder      []*QueryNode

	// batcher, when the connection can pipeline, makes run* queue statements ; flush sends them in one round trip.
	batcher Batcher
	queue   []queuedStmt

	// format is how a fast write's response rows are produced (WriteOptions.Format).
	format OutputFormat

	// fast, when set, makes run* write straight from the request's rows and answer from RETURNING, with no temp table.
	fast *fastWrite

	// nextRowID is the first free row id ; runUpdateWhere allocates one per updated row.
	nextRowID int
	// patchRowID is the row holding an "update" root's patch, and patchMask its presence words.
	patchRowID int
	patchMask  []int64
}

// tempOf returns the general temp table of node's relation.
func (dc *dmlCompiler) tempOf(node *QueryNode) *tempTable {
	return dc.temps[node.Relation]
}

// soleNode reports whether every row in node's temp table belongs to node, so statements can drop the
// "node_id = N" filter : without statistics on a temp table, the planner guesses it keeps 0.5% of rows and picks a nested loop.
func (dc *dmlCompiler) soleNode(node *QueryNode) bool {
	if dc.fast != nil {
		return true
	}
	nodes := dc.tableNodes[node.Relation]
	return len(nodes) == 1 && nodes[dc.ids[node]]
}

// writeWhere writes " where a and b", or nothing when there is no condition.
func writeWhere(w *writer.SQLWriter, conds []string) {
	for i, c := range conds {
		if i == 0 {
			w.Write(" where ")
		} else {
			w.Write(" and ")
		}
		w.Write(c)
	}
}

// nodeCond is the "node_id = N" condition on alias's rows for node, or nothing when node owns the whole table.
func (dc *dmlCompiler) nodeCond(node *QueryNode, alias string) []string {
	if dc.soleNode(node) {
		return nil
	}
	return []string{fmt.Sprintf("%s = %d", dc.tempOf(node).nodeIDRef(alias), dc.ids[node])}
}

// sqlEntry get-or-creates node's SqlResult entry.
func (dc *dmlCompiler) sqlEntry(node *QueryNode) *SqlResult {
	if s, ok := dc.sqlByNode[node]; ok {
		return s
	}
	s := &SqlResult{Path: nodeStatPath(node)}
	if dc.sqlByNode == nil {
		dc.sqlByNode = map[*QueryNode]*SqlResult{}
	}
	dc.sqlByNode[node] = s
	dc.sqlOrder = append(dc.sqlOrder, node)
	return s
}

// finalSQL orders sqlByNode by first-touch ; nil when collectSQL was never set.
func (dc *dmlCompiler) finalSQL() []SqlResult {
	if !dc.collectSQL {
		return nil
	}
	out := make([]SqlResult, len(dc.sqlOrder))
	for i, n := range dc.sqlOrder {
		out[i] = *dc.sqlByNode[n]
	}
	return out
}

// planEntry get-or-creates node's PlanResult entry.
func (dc *dmlCompiler) planEntry(node *QueryNode) *PlanResult {
	if p, ok := dc.planByNode[node]; ok {
		return p
	}
	p := &PlanResult{Path: nodeStatPath(node)}
	if dc.planByNode == nil {
		dc.planByNode = map[*QueryNode]*PlanResult{}
	}
	dc.planByNode[node] = p
	dc.planOrder = append(dc.planOrder, node)
	return p
}

// finalPlans orders planByNode by first-touch ; nil when explainAnalyze was never set.
func (dc *dmlCompiler) finalPlans() []PlanResult {
	if !dc.explainAnalyze {
		return nil
	}
	out := make([]PlanResult, len(dc.planOrder))
	for i, n := range dc.planOrder {
		out[i] = *dc.planByNode[n]
	}
	return out
}

// exec runs a run*'s main DML statement (never temp-table housekeeping) : records its text when collectSQL, and
// when explainAnalyze runs it under EXPLAIN ANALYZE, for real, since later statements depend on its side effects.
func (dc *dmlCompiler) exec(ctx context.Context, node *QueryNode, kind string, w *writer.SQLWriter, args []any) (pgconn.CommandTag, error) {
	if dc.collectSQL {
		dc.recordSQL(node, kind, w.String())
	}
	if dc.explainAnalyze {
		raw, err := dc.runExplainAnalyze(ctx, w.String(), args)
		if err != nil {
			return pgconn.CommandTag{}, err
		}
		dc.recordPlan(node, kind, raw)
		return pgconn.CommandTag{}, nil
	}
	return dc.conn.Exec(ctx, w.String(), args...)
}

// Batcher is the optional connection capability that lets a write send all its
// statements in one round trip ; *pgxpool.Conn and *pgx.Conn have it.
type Batcher interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// queuedStmt is a statement waiting for flush ; after, if set, receives its command tag.
type queuedStmt struct {
	node  *QueryNode
	kind  string
	sql   string
	args  []any
	after func(pgconn.CommandTag)
}

// submit is exec for a statement whose only outcome Go needs is its command tag : queued with a Batcher, otherwise
// (or under EXPLAIN ANALYZE) run now. after runs once the statement has run, before later statements are sent.
func (dc *dmlCompiler) submit(ctx context.Context, node *QueryNode, kind string, w *writer.SQLWriter, args []any, after func(pgconn.CommandTag)) error {
	if dc.batcher == nil || dc.explainAnalyze {
		if err := dc.flush(ctx); err != nil {
			return err
		}
		tag, err := dc.exec(ctx, node, kind, w, args)
		if err != nil {
			return oops.With("sql", w.String()).Wrapf(err, "%s", kind)
		}
		if after != nil {
			after(tag)
		}
		return nil
	}
	if dc.collectSQL {
		dc.recordSQL(node, kind, w.String())
	}
	dc.queue = append(dc.queue, queuedStmt{node: node, kind: kind, sql: w.String(), args: args, after: after})
	return nil
}

// enqueue is submit for temp-table housekeeping (loading rows) : never recorded under sql/query_plan, never
// wrapped in EXPLAIN ANALYZE.
func (dc *dmlCompiler) enqueue(ctx context.Context, node *QueryNode, label, sql string, args []any) error {
	if dc.batcher == nil {
		if _, err := dc.conn.Exec(ctx, sql, args...); err != nil {
			return oops.With("sql", sql).Wrapf(err, "%s", label)
		}
		return nil
	}
	dc.queue = append(dc.queue, queuedStmt{node: node, kind: label, sql: sql, args: args})
	return nil
}

// flush sends every queued statement in one round trip, in order, and runs their after callbacks. The first
// failure stops everything after it and is reported with the statement that caused it.
func (dc *dmlCompiler) flush(ctx context.Context) error {
	if len(dc.queue) == 0 {
		return nil
	}
	queue := dc.queue
	dc.queue = nil
	var b pgx.Batch
	for _, q := range queue {
		b.Queue(q.sql, q.args...)
	}
	res := dc.batcher.SendBatch(ctx, &b)
	for _, q := range queue {
		tag, err := res.Exec()
		if err != nil {
			_ = res.Close()
			name := ""
			if q.node != nil {
				name = q.node.InnerName
			}
			return oops.With("node", name, "sql", q.sql).Wrapf(err, "write: %s", q.kind)
		}
		if q.after != nil {
			q.after(tag)
		}
	}
	return res.Close()
}

// recordSQL assigns raw to kind's field on node's SqlResult.
func (dc *dmlCompiler) recordSQL(node *QueryNode, kind, raw string) {
	e := dc.sqlEntry(node)
	switch kind {
	case "insert":
		e.Insert = raw
	case "update":
		e.Update = raw
	case "upsert":
		e.Upsert = raw
	case "delete":
		e.Delete = raw
	}
}

// recordPlan assigns raw to kind's field on node's PlanResult.
func (dc *dmlCompiler) recordPlan(node *QueryNode, kind string, raw json.RawMessage) {
	e := dc.planEntry(node)
	switch kind {
	case "insert":
		e.Insert = raw
	case "update":
		e.Update = raw
	case "upsert":
		e.Upsert = raw
	case "delete":
		e.Delete = raw
	}
}

// runExplainAnalyze runs sql under EXPLAIN (ANALYZE, FORMAT JSON), side effects included, and returns the plan JSON.
func (dc *dmlCompiler) runExplainAnalyze(ctx context.Context, sql string, args []any) (json.RawMessage, error) {
	rows, err := dc.conn.Query(ctx, "explain (analyze, format json)\n"+sql, args...)
	if err != nil {
		return nil, oops.With("sql", sql).Wrapf(err, "explain analyze")
	}
	defer rows.Close()
	var raw []byte
	if rows.Next() {
		if err := rows.Scan(&raw); err != nil {
			return nil, oops.Wrapf(err, "explain analyze: scanning plan")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, oops.Wrapf(err, "explain analyze")
	}
	return json.RawMessage(raw), nil
}

// nodeStatPath is the chain of join-alias keys from root down to node ; [] for the root.
func nodeStatPath(node *QueryNode) []string {
	path := []string{}
	for n := node; n.Parent != nil; n = n.Parent {
		path = append([]string{n.OuterAlias}, path...)
	}
	return path
}

// stat get-or-creates node's Stat entry, which marks the node as touched ; call it only when collectStats.
func (dc *dmlCompiler) stat(node *QueryNode) *Stat {
	if s, ok := dc.statsByNode[node]; ok {
		return s
	}
	s := &Stat{
		Path:      nodeStatPath(node),
		Table:     node.Relation.Identifier.String(),
		Submitted: dc.submitted[dc.ids[node]],
	}
	if dc.statsByNode == nil {
		dc.statsByNode = map[*QueryNode]*Stat{}
	}
	dc.statsByNode[node] = s
	dc.statOrder = append(dc.statOrder, node)
	return s
}

// finalStats orders statsByNode by first-touch ; nil when collectStats was never set.
func (dc *dmlCompiler) finalStats() []Stat {
	if !dc.collectStats {
		return nil
	}
	out := make([]Stat, len(dc.statOrder))
	for i, n := range dc.statOrder {
		out[i] = *dc.statsByNode[n]
	}
	return out
}

// ---- traversal --------------------------------------------------------------------

// phase1 : outgoing children, then this node's own DML, then incoming children ; an unpopulated subtree is skipped.
func (dc *dmlCompiler) phase1(ctx context.Context, node *QueryNode) error {
	nodeID, ok := dc.ids[node]
	if !ok {
		return nil
	}
	if !dc.populated[nodeID] {
		return nil
	}
	for _, c := range node.OutgoingNodes {
		if err := dc.phase1(ctx, c); err != nil {
			return err
		}
	}
	if err := dc.runPhase1Node(ctx, node); err != nil {
		return oops.With("node", node.InnerName).Wrapf(err, "write")
	}
	for _, c := range node.IncomingNodes {
		if err := dc.phase1(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

// phase2 : incoming children deepest-first, then this node's own delete. Delete gates on the parent's population :
// an absent subtree means "delete everything here", not "skip".
func (dc *dmlCompiler) phase2(ctx context.Context, node *QueryNode, parent *QueryNode) error {
	nodeID, ok := dc.ids[node]
	if !ok {
		return nil
	}
	if dc.populated[nodeID] {
		for _, c := range node.IncomingNodes {
			if err := dc.phase2(ctx, c, node); err != nil {
				return err
			}
		}
		for _, c := range node.OutgoingNodes {
			if err := dc.phase2(ctx, c, node); err != nil {
				return err
			}
		}
	}
	if hasDeleteComponent(node.WriteMode) {
		if parent == nil || dc.populated[dc.ids[parent]] {
			if err := dc.runDelete(ctx, node, parent); err != nil {
				return oops.With("node", node.InnerName).Wrapf(err, "write: delete")
			}
		}
	}
	return nil
}

// args resolves w's bind slots against dc.paramValues.
func (dc *dmlCompiler) args(w *writer.SQLWriter) ([]any, error) {
	return w.ResolveArgs(dc.paramValues)
}

// ---- shared column-set helpers -----------------------------------------------------

// identityColumns is on_conflict's columns if set, else the primary key.
func identityColumns(node *QueryNode) []*pg.Column {
	if len(node.OnConflictColumns) > 0 {
		cols := make([]*pg.Column, 0, len(node.OnConflictColumns))
		for _, name := range node.OnConflictColumns {
			if c := node.Relation.ColumnsMap[name]; c != nil {
				cols = append(cols, c)
			}
		}
		return cols
	}
	if node.Relation.PrimaryKey != nil {
		return node.Relation.PrimaryKey.Columns
	}
	return nil
}

// requireIdentityColumns is identityColumns, rejecting an empty result ; op names the operation in the error.
func requireIdentityColumns(node *QueryNode, op string) ([]*pg.Column, error) {
	identCols := identityColumns(node)
	if len(identCols) == 0 {
		return nil, oops.With("relation", node.Relation.Identifier.String()).Errorf("%s requires an identity (on_conflict or primary key) column set", op)
	}
	return identCols, nil
}

// columnsFor applies an insert_columns/update_columns allowlist (empty = no filtering) to node.Shape.Extractors.
func columnsFor(node *QueryNode, allowlist []string) ([]ColumnPath, error) {
	var cols []ColumnPath
	seen := map[string]bool{}
	var allowed map[string]bool
	if len(allowlist) > 0 {
		allowed = make(map[string]bool, len(allowlist))
		for _, n := range allowlist {
			allowed[n] = true
		}
	}
	for _, ex := range node.Shape.Extractors {
		if allowed != nil && !allowed[ex.Path.Path[0].Name] {
			continue
		}
		key := ex.Path.Key()
		if !seen[key] {
			seen[key] = true
			cols = append(cols, ex.Path)
		}
	}
	for _, col := range identityColumns(node) {
		cp := ColumnPath{Node: node, Path: []*pg.Column{col}}
		key := cp.Key()
		if !seen[key] && col.DefaultExpression != "" {
			seen[key] = true
			cols = append(cols, cp)
		}
	}
	// Outgoing-child FK columns are never in Shape.Extractors, so they bypass the allowlist like identity columns do.
	for _, c := range node.OutgoingNodes {
		for _, jc := range c.JoinColumns {
			cp := ColumnPath{Node: node, Path: []*pg.Column{jc.Distant}}
			key := cp.Key()
			if !seen[key] {
				seen[key] = true
				cols = append(cols, cp)
			}
		}
	}
	// Likewise an incoming child's own FK column to its parent : the join's
	// "on" already names it, so it is written whether or not select lists it.
	if node.Parent != nil && isIncoming(node.Parent, node) {
		for _, jc := range node.JoinColumns {
			cp := ColumnPath{Node: node, Path: []*pg.Column{jc.Local}}
			key := cp.Key()
			if !seen[key] {
				seen[key] = true
				cols = append(cols, cp)
			}
		}
	}
	return cols, nil
}

// wrapColumns wraps plain columns as single-segment ColumnPaths.
func wrapColumns(node *QueryNode, cols []*pg.Column) []ColumnPath {
	out := make([]ColumnPath, len(cols))
	for i, c := range cols {
		out[i] = ColumnPath{Node: node, Path: []*pg.Column{c}}
	}
	return out
}

// withKeysColumns unions cols with keysColumns(node).
func withKeysColumns(node *QueryNode, cols []ColumnPath) []ColumnPath {
	return dedupeColumnPaths(cols, wrapColumns(node, keysColumns(node)))
}

// outgoingKeySource finds the outgoing child whose recovered key supplies
// node's col (the outgoing "on" mapping, parent's side).
func outgoingKeySource(node *QueryNode, col *pg.Column) (child *QueryNode, keyCol *pg.Column) {
	for _, c := range node.OutgoingNodes {
		for _, jc := range c.JoinColumns {
			if jc.Distant == col {
				return c, jc.Local
			}
		}
	}
	return nil, nil
}

// incomingKeySource reports whether col is node's own FK column to its parent in the payload, node being incoming.
func incomingKeySource(node *QueryNode, col *pg.Column) (keyCol *pg.Column, ok bool) {
	if node.Parent == nil {
		return nil, false
	}
	if !isIncoming(node.Parent, node) {
		return nil, false
	}
	for _, jc := range node.JoinColumns {
		if jc.Local == col {
			return jc.Distant, true
		}
	}
	return nil, false
}

// ---- resolved CTE -------------------------------------------------------------------

// writeResolvedCTE writes "resolved as (select ...)" (no leading "with"), one row per temp row. withMasks also
// exposes the row's presence words as "__m0", "__m1", ..., to tell an absent column from a supplied one.
func (dc *dmlCompiler) writeResolvedCTE(w *writer.SQLWriter, node *QueryNode, cols []ColumnPath, withMasks bool) error {
	t := dc.tempOf(node)

	needsPar := false
	outgoingAliases := map[*QueryNode]string{}
	var outgoingOrder []*QueryNode // deterministic emission order
	for _, cp := range cols {
		if len(cp.Path) != 1 {
			continue // a composite sub-field is never FK-linkage
		}
		col := cp.Path[0]
		if _, ok := incomingKeySource(node, col); ok {
			needsPar = true
		}
		if child, _ := outgoingKeySource(node, col); child != nil {
			if _, ok := outgoingAliases[child]; !ok {
				outgoingAliases[child] = fmt.Sprintf("oc%d", len(outgoingAliases))
				outgoingOrder = append(outgoingOrder, child)
			}
		}
	}

	w.Write("resolved as (\n")
	w.Indent()
	w.Write("select\n")
	w.Indent()
	w.Write(t.rowIDRef("tmp"))
	w.Write(" as __row_id")
	if withMasks {
		for i := 0; i < t.maskWords; i++ {
			w.Write(",\n")
			w.Write(t.maskWordRef("tmp", i))
			w.Write(fmt.Sprintf(" as __m%d", i))
		}
	}
	for _, cp := range cols {
		w.Write(",\n")
		if err := dc.writeColumnCase(w, node, cp, outgoingAliases); err != nil {
			return err
		}
		w.Write(" as ")
		w.Id(columnPathFlatName(cp))
	}
	w.Unindent()
	w.Write("\n")
	if dc.fast != nil {
		w.Write("from tmp\n") // the CTE built from the request's rows
	} else {
		w.Write("from ")
		w.Write(t.qualifiedGeneral())
		w.Write(" tmp\n")
	}
	if needsPar {
		pt := dc.tempOf(node.Parent)
		w.Write("inner join ")
		w.Write(pt.qualifiedGeneral())
		w.Write(" par on ")
		w.Write(pt.rowIDRef("par"))
		w.Write(" = ")
		w.Write(t.parentIDRef("tmp"))
		w.Write("\n")
	}
	for _, child := range outgoingOrder {
		alias := outgoingAliases[child]
		ct := dc.tempOf(child)
		w.Write("left join ")
		w.Write(ct.qualifiedGeneral())
		w.Write(" ")
		w.Write(alias)
		w.Write(" on ")
		if !dc.soleNode(child) {
			w.Write(ct.nodeIDRef(alias))
			w.Write(" = ")
			w.Write(fmt.Sprintf("%d", dc.ids[child]))
			w.Write(" and ")
		}
		w.Write(ct.parentIDRef(alias))
		w.Write(" = ")
		w.Write(t.rowIDRef("tmp"))
		w.Write("\n")
	}
	if conds := dc.nodeCond(node, "tmp"); conds != nil {
		writeWhere(w, conds)
		w.Write("\n")
	}
	w.Unindent()
	w.Write(")")
	return nil
}

// writeColumnCase writes the 3-way resolution rule for a plain column ; a composite sub-field always reads its
// field of the temp row's composite value.
func (dc *dmlCompiler) writeColumnCase(w *writer.SQLWriter, node *QueryNode, cp ColumnPath, outgoingAliases map[*QueryNode]string) error {
	t := dc.tempOf(node)
	if len(cp.Path) > 1 {
		w.Write(t.cpRef("tmp", cp.Path))
		return nil
	}
	col := cp.Path[0]
	typeName := col.Type.PgIdentifier.EscapedString()

	if distant, ok := incomingKeySource(node, col); ok {
		w.Write("(")
		w.Write(dc.tempOf(node.Parent).colRef("par", distant))
		w.Write(")::")
		w.Write(typeName)
		return nil
	}
	if child, keyCol := outgoingKeySource(node, col); child != nil {
		w.Write("(")
		w.Write(dc.tempOf(child).colRef(outgoingAliases[child], keyCol))
		w.Write(")::")
		w.Write(typeName)
		return nil
	}

	if col.DefaultExpression != "" {
		slot, ok := node.Relation.MaskSlotFor(col)
		if !ok {
			return oops.With("relation", node.Relation.Identifier.String(), "column", col.Name).Errorf("write: no presence slot for column %q", col.Name)
		}
		w.Write("case when not ")
		w.Write(t.maskTest("tmp", slot))
		if col.IsReallyNotNull() { // an explicit null in a NOT NULL column takes the default
			w.Write(" or ")
			w.Write(t.rawColRef("tmp", col))
			w.Write(" is null")
		}
		w.Write(" then ")
		w.Write(col.DefaultExpression)
		w.Write(" else ")
		w.Write(t.colRef("tmp", col))
		w.Write(" end")
		return nil
	}

	w.Write(t.colRef("tmp", col))
	return nil
}

// writeUpdateValue writes the new value of cp in runUpdate's SET : the payload's
// value when the row supplies the column, the row's current value when it does
// not. A column filled from a parent/child key is always supplied.
func (dc *dmlCompiler) writeUpdateValue(w *writer.SQLWriter, node *QueryNode, cp ColumnPath) {
	flat := columnPathFlatName(cp)
	if len(cp.Path) == 1 && isKeyLinked(node, cp.Path[0]) {
		w.Write("resolved.")
		w.Id(flat)
		return
	}
	slot, _ := node.Relation.MaskSlotFor(cp.Path...)
	w.Write(fmt.Sprintf("case when (resolved.__m%d & %s::int8) <> 0 then resolved.", slot.Word, maskBitLiteral(slot.Bit)))
	w.Id(flat)
	w.Write(" else ")
	WriteQualifiedPath(w, "t", cp.Path)
	w.Write(" end")
}

// isKeyLinked reports whether col gets its value from a parent's or an outgoing child's recovered key, not the payload.
func isKeyLinked(node *QueryNode, col *pg.Column) bool {
	if _, ok := incomingKeySource(node, col); ok {
		return true
	}
	child, _ := outgoingKeySource(node, col)
	return child != nil
}

// keysColumns is identityColumns(node) plus every column an incoming child's JoinColumns needs.
func keysColumns(node *QueryNode) []*pg.Column {
	cols := append([]*pg.Column(nil), identityColumns(node)...)
	seen := map[*pg.Column]bool{}
	for _, c := range cols {
		seen[c] = true
	}
	for _, child := range node.IncomingNodes {
		for _, jc := range child.JoinColumns {
			if !seen[jc.Distant] {
				seen[jc.Distant] = true
				cols = append(cols, jc.Distant)
			}
		}
	}
	// As an outgoing child, node's jc.Local may be any unique column, not in identityColumns(node).
	if node.Parent != nil && isOutgoingOf(node.Parent, node) {
		for _, jc := range node.JoinColumns {
			if !seen[jc.Local] {
				seen[jc.Local] = true
				cols = append(cols, jc.Local)
			}
		}
	}
	return cols
}

// writeKeyList writes node's key columns read from src ("src.a, src.b"), for a select or RETURNING list.
func writeKeyList(w *writer.SQLWriter, node *QueryNode, src string) {
	for i, col := range keysColumns(node) {
		if i > 0 {
			w.Write(", ")
		}
		w.Write(src)
		w.Write(".")
		w.Id(col.Name)
	}
}

// writeKeySet writes the assignments setting node's key columns in its temp row from src's columns of the same name.
func (dc *dmlCompiler) writeKeySet(w *writer.SQLWriter, node *QueryNode, src string) {
	t := dc.tempOf(node)
	for i, col := range keysColumns(node) {
		if i > 0 {
			w.Write(", ")
		}
		w.Write(t.assignKey(col, src))
	}
}

// ---- phase 1 dispatch ----------------------------------------------------------------

func (dc *dmlCompiler) runPhase1Node(ctx context.Context, node *QueryNode) error {
	switch node.WriteMode {
	case READONLY, DELETE_ONLY, DELETE:
		return nil
	case INSERT, MERGE_NEW:
		return dc.runInsert(ctx, node, node.WriteMode == MERGE_NEW)
	case UPDATE_EXISTING, MERGE_UPDATE:
		return dc.runUpdate(ctx, node)
	case UPDATE:
		return dc.runUpdateWhere(ctx, node)
	case UPSERT, MERGE:
		return dc.runUpsert(ctx, node)
	default:
		return oops.With("relation", node.Relation.Identifier.String()).Errorf("unhandled write_mode %v", node.WriteMode)
	}
}

// runInsert : plain insert, or with doNothing (MERGE_NEW) "on conflict do nothing" plus recovery of existing keys.
func (dc *dmlCompiler) runInsert(ctx context.Context, node *QueryNode, doNothing bool) error {
	cols, err := columnsFor(node, node.InsertColumns)
	if err != nil {
		return err
	}
	// Every keysColumns member must be in both the resolved CTE and the INSERT column list, or the statement fails or
	// a generated key differs from the one written back.
	cols = withKeysColumns(node, cols)
	w := writer.NewSQL()
	if err := dc.writeWith(w, node); err != nil {
		return err
	}
	if err := dc.writeResolvedCTE(w, node, cols, false); err != nil {
		return err
	}
	w.Write(",\nins as (\n")
	w.Indent()
	w.Write("insert into ")
	w.Write(node.Relation.Identifier.EscapedString())
	w.Write(" (")
	for i, cp := range cols {
		if i > 0 {
			w.Write(", ")
		}
		writeTargetPath(w, cp)
	}
	w.Write(")\n")
	w.Write("overriding system value\n")
	w.Write("select ")
	for i, cp := range cols {
		if i > 0 {
			w.Write(", ")
		}
		w.Write("resolved.")
		w.Id(columnPathFlatName(cp))
	}
	w.Write(" from resolved")
	if dc.fast != nil {
		w.Write(" order by resolved.__row_id") // RETURNING comes back in insertion order
	}
	w.Write("\n")
	identCols := identityColumns(node)
	if dc.fast != nil {
		w.Write("returning *\n")
	}
	if doNothing {
		w.Write("on conflict (")
		for i, col := range identCols {
			if i > 0 {
				w.Write(", ")
			}
			w.Id(col.Name)
		}
		w.Write(") do nothing\n")
		w.Write("returning ")
		for i, col := range keysColumns(node) {
			if i > 0 {
				w.Write(", ")
			}
			w.Id(col.Name)
		}
		w.Write("\n")
	}
	w.Unindent()
	w.Write(")\n")
	if dc.fast != nil {
		return dc.finishFast(ctx, node, w, "ins")
	}
	dc.writeInsertKeyWriteBack(w, node, identCols, doNothing)

	args, err := dc.args(w)
	if err != nil {
		return err
	}
	if err := dc.submit(ctx, node, "insert", w, args, func(tag pgconn.CommandTag) {
		if dc.collectStats {
			// The key write-back only joins inserted rows, so its RowsAffected() is the insert count.
			dc.stat(node).Inserted += int(tag.RowsAffected())
		}
	}); err != nil {
		return err
	}

	if doNothing {
		return dc.recoverKeys(ctx, node)
	}
	return nil
}

// recoverKeys handles MERGE_NEW's "do nothing" branch : fills the key columns of a conflicting
// row's temp row from the existing row, found by identity match. Rows whose key columns already
// hold what the existing row has are left alone.
func (dc *dmlCompiler) recoverKeys(ctx context.Context, node *QueryNode) error {
	identCols := identityColumns(node)
	if len(identCols) == 0 {
		return nil
	}
	tt := dc.tempOf(node)
	keyCols := keysColumns(node)

	w := writer.NewSQL()
	w.Write("update ")
	w.Write(tt.qualifiedGeneral())
	w.Write(" w\n")
	w.Write("set ")
	for i, col := range keyCols {
		if i > 0 {
			w.Write(", ")
		}
		w.Write(tt.assignKey(col, "t"))
	}
	w.Write("\nfrom ")
	w.Write(node.Relation.Identifier.EscapedString())
	w.Write(" t\n")
	conds := dc.nodeCond(node, "w")
	for _, col := range identCols {
		conds = append(conds, "t."+quoteIdent(col.Name)+" = "+tt.colRef("w", col))
	}
	var supplied, existing []string
	for _, col := range keyCols {
		supplied = append(supplied, tt.colRef("w", col))
		existing = append(existing, "t."+quoteIdent(col.Name))
	}
	conds = append(conds, "("+strings.Join(supplied, ", ")+") is distinct from ("+strings.Join(existing, ", ")+")")
	writeWhere(w, conds)

	args, err := dc.args(w)
	if err != nil {
		return err
	}
	return dc.enqueue(ctx, node, "recovering merge-new keys", w.String(), args)
}

// runUpdate : a single update ... from ... returning, since the row's identity is known before it runs.
func (dc *dmlCompiler) runUpdate(ctx context.Context, node *QueryNode) error {
	updateAllowlist := node.UpdateColumns
	if len(updateAllowlist) == 0 {
		updateAllowlist = node.InsertColumns
	}
	cols, err := columnsFor(node, updateAllowlist)
	if err != nil {
		return err
	}
	identCols, err := requireIdentityColumns(node, "update")
	if err != nil {
		return err
	}

	w := writer.NewSQL()
	if err := dc.writeWith(w, node); err != nil {
		return err
	}
	if err := dc.writeResolvedCTE(w, node, dedupeColumnPaths(cols, wrapColumns(node, identCols)), true); err != nil {
		return err
	}
	w.Write(",\ndml as (\n")
	w.Indent()
	// The identity columns locate the row, so they are never SET : "generated always" rejects even an unchanged value.
	// A primary key that is not the matching key can be changed.
	setCols := excludeColumnPaths(cols, identCols)
	writeIdentMatch := func() {
		for i, col := range identCols {
			if i > 0 {
				w.Write(" and ")
			}
			w.Write("t.")
			w.Id(col.Name)
			w.Write(" = resolved.")
			w.Id(col.Name)
		}
	}
	if len(setCols) == 0 {
		// Nothing to change (only identity columns, e.g. a parent kept for its children) : read the row's keys instead.
		w.Write("select resolved.__row_id, ")
		if dc.fast != nil {
			w.Write("t.*")
		} else {
			writeKeyList(w, node, "t")
		}
		w.Write(" from resolved join ")
		w.Write(node.Relation.Identifier.EscapedString())
		w.Write(" t on ")
		writeIdentMatch()
	} else {
		w.Write("update ")
		w.Write(node.Relation.Identifier.EscapedString())
		w.Write(" t\n")
		w.Write("set ")
		for i, cp := range setCols {
			if i > 0 {
				w.Write(", ")
			}
			writeTargetPath(w, cp)
			w.Write(" = ")
			dc.writeUpdateValue(w, node, cp)
		}
		w.Write("\n")
		w.Write("from resolved\n")
		w.Write("where ")
		writeIdentMatch()
		w.Write("\n")
		// Keys are read off the updated row "t", so keysColumns' extras need not be in the SET list.
		w.Write("returning resolved.__row_id, ")
		if dc.fast != nil {
			w.Write("t.*")
		} else {
			writeKeyList(w, node, "t")
		}
	}
	w.Unindent()
	w.Write("\n)\n")
	if dc.fast != nil {
		return dc.finishFast(ctx, node, w, "dml")
	}
	// Folded into "dml" itself, so nothing needs RETURNING back in Go and it stays usable under EXPLAIN ANALYZE.
	tt := dc.tempOf(node)
	w.Write("update ")
	w.Write(tt.qualifiedGeneral())
	w.Write(" w\nset ")
	dc.writeKeySet(w, node, "dml")
	w.Write("\nfrom dml\nwhere ")
	w.Write(tt.rowIDRef("w"))
	w.Write(" = dml.__row_id")

	args, err := dc.args(w)
	if err != nil {
		return err
	}
	if err := dc.submit(ctx, node, "update", w, args, func(tag pgconn.CommandTag) {
		if dc.collectStats {
			dc.stat(node).Updated += int(tag.RowsAffected())
		}
	}); err != nil {
		return err
	}
	return nil
}

// runUpsert : insert ... on conflict (...) do update ... returning, matched back by the on_conflict columns, which
// are known before the insert, unlike a generated primary key.
func (dc *dmlCompiler) runUpsert(ctx context.Context, node *QueryNode) error {
	identCols, err := requireIdentityColumns(node, "upsert")
	if err != nil {
		return err
	}
	insertCols, err := columnsFor(node, node.InsertColumns)
	if err != nil {
		return err
	}
	// As in runInsert, every keysColumns member must be in the resolved CTE and written by the insert side.
	insertCols = withKeysColumns(node, insertCols)
	updateAllowlist := node.UpdateColumns
	if len(updateAllowlist) == 0 {
		updateAllowlist = node.InsertColumns
	}
	updateCols, err := columnsFor(node, updateAllowlist)
	if err != nil {
		return err
	}
	// Never update the identity columns (the conflict target) from excluded.*, nor the primary key even when
	// on_conflict names another constraint : excluded.<pk> may be a fresh nextval(), not the matched row's.
	exclude := identCols
	if node.Relation.PrimaryKey != nil {
		exclude = append(append([]*pg.Column(nil), identCols...), node.Relation.PrimaryKey.Columns...)
	}
	updateCols = excludeColumnPaths(updateCols, exclude)

	w := writer.NewSQL()
	if err := dc.writeWith(w, node); err != nil {
		return err
	}
	if err := dc.writeResolvedCTE(w, node, insertCols, false); err != nil {
		return err
	}
	w.Write(",\ndml as (\n")
	w.Indent()
	w.Write("insert into ")
	w.Write(node.Relation.Identifier.EscapedString())
	w.Write(" (")
	for i, cp := range insertCols {
		if i > 0 {
			w.Write(", ")
		}
		writeTargetPath(w, cp)
	}
	w.Write(")\n")
	w.Write("overriding system value\n")
	w.Write("select ")
	for i, cp := range insertCols {
		if i > 0 {
			w.Write(", ")
		}
		w.Write("resolved.")
		w.Id(columnPathFlatName(cp))
	}
	w.Write(" from resolved")
	if dc.fast != nil {
		w.Write(" order by resolved.__row_id")
	}
	w.Write("\n")
	w.Write("on conflict (")
	for i, col := range identCols {
		if i > 0 {
			w.Write(", ")
		}
		w.Id(col.Name)
	}
	w.Write(") do ")
	if len(updateCols) == 0 {
		// Nothing to update ; a no-op update is still needed for RETURNING to see the conflicting row. It sets a non-key
		// column to itself, since a generated-always primary key rejects any SET.
		var pkOnly []*pg.Column
		if node.Relation.PrimaryKey != nil {
			pkOnly = node.Relation.PrimaryKey.Columns
		}
		noop := firstColumnNotIn(node.Relation.Columns, pkOnly)
		if noop == nil {
			return oops.With("relation", node.Relation.Identifier.String()).Errorf("upsert: every column is part of the primary key, leaving no column for a no-op ON CONFLICT DO UPDATE")
		}
		w.Write("update set ")
		w.Id(noop.Name)
		w.Write(" = ")
		w.Write(node.Relation.Identifier.EscapedString())
		w.Write(".")
		w.Id(noop.Name)
	} else {
		w.Write("update set ")
		for i, cp := range updateCols {
			if i > 0 {
				w.Write(", ")
			}
			writeTargetPath(w, cp)
			w.Write(" = ")
			// "excluded" is a row : a composite sub-field needs row-value parenthesization.
			WriteQualifiedPath(w, "excluded", cp.Path)
		}
	}
	w.Write("\n")
	if dc.fast != nil {
		w.Write("returning *")
		w.Unindent()
		w.Write("\n)\n")
		return dc.finishFast(ctx, node, w, "dml")
	}
	w.Write("returning ")
	for i, col := range keysColumns(node) {
		if i > 0 {
			w.Write(", ")
		}
		w.Id(col.Name)
	}
	// __inserted splits inserts from updates for collectStats ; the command tag can't.
	w.Write(", (xmax = 0) as __inserted")
	w.Unindent()
	w.Write("\n)")

	if !dc.collectStats {
		w.Write("\n")
		dc.writeUpsertKeyWriteBack(w, node, identCols)
		args, err := dc.args(w)
		if err != nil {
			return err
		}
		return dc.submit(ctx, node, "upsert", w, args, nil)
	}

	// collectStats : the key write-back becomes the CTE "upd", so the outer statement can be a SELECT reading counts.
	w.Write(",\nupd as (\n")
	w.Indent()
	dc.writeUpsertKeyWriteBack(w, node, identCols)
	w.Write("\nreturning 1")
	w.Unindent()
	w.Write("\n)\n")
	w.Write("select\n")
	w.Write("  (select count(*) from upd) as touched,\n")
	w.Write("  (select count(*) filter (where __inserted) from dml) as inserted,\n")
	w.Write("  (select count(*) filter (where not __inserted) from dml) as updated")

	if dc.collectSQL {
		dc.recordSQL(node, "upsert", w.String())
	}
	args, err := dc.args(w)
	if err != nil {
		return err
	}
	if err := dc.flush(ctx); err != nil { // its counts are read back right now
		return err
	}
	rows, err := dc.conn.Query(ctx, w.String(), args...)
	if err != nil {
		return oops.With("sql", w.String()).Wrapf(err, "upsert")
	}
	var touched, inserted, updated int
	if rows.Next() {
		if err := rows.Scan(&touched, &inserted, &updated); err != nil {
			rows.Close()
			return oops.Wrapf(err, "upsert: scanning counts")
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return oops.With("sql", w.String()).Wrapf(err, "upsert")
	}
	s := dc.stat(node)
	s.Inserted += inserted
	s.Updated += updated
	return nil
}

// writeInsertKeyWriteBack writes runInsert's key write-back. With doNothing the keys come from "ins" joined by
// identity columns, since only inserted rows are returned ; otherwise straight from "resolved".
func (dc *dmlCompiler) writeInsertKeyWriteBack(w *writer.SQLWriter, node *QueryNode, identCols []*pg.Column, doNothing bool) {
	tt := dc.tempOf(node)
	src := "resolved"
	if doNothing {
		src = "ins"
	}
	w.Write("update ")
	w.Write(tt.qualifiedGeneral())
	w.Write(" w\nset ")
	dc.writeKeySet(w, node, "r")
	w.Write("\nfrom (select resolved.__row_id, ")
	writeKeyList(w, node, src)
	w.Write(" from resolved")
	if doNothing {
		w.Write(" join ins on ")
		for i, col := range identCols {
			if i > 0 {
				w.Write(" and ")
			}
			w.Write("ins.")
			w.Id(col.Name)
			w.Write(" = resolved.")
			w.Id(col.Name)
		}
	}
	w.Write(") r\nwhere ")
	w.Write(tt.rowIDRef("w"))
	w.Write(" = r.__row_id")
}

// writeUpsertKeyWriteBack writes runUpsert's key write-back, with or without stats.
func (dc *dmlCompiler) writeUpsertKeyWriteBack(w *writer.SQLWriter, node *QueryNode, identCols []*pg.Column) {
	tt := dc.tempOf(node)
	w.Write("update ")
	w.Write(tt.qualifiedGeneral())
	w.Write(" w\nset ")
	dc.writeKeySet(w, node, "dml")
	w.Write("\nfrom resolved\njoin dml on ")
	for i, col := range identCols {
		if i > 0 {
			w.Write(" and ")
		}
		w.Write("dml.")
		w.Id(col.Name)
		w.Write(" = resolved.")
		w.Id(col.Name)
	}
	w.Write("\nwhere ")
	w.Write(tt.rowIDRef("w"))
	w.Write(" = resolved.__row_id")
}

// dedupeColumnPaths dedupes by ColumnPath.Key() : two composite sub-fields can share the same leaf *pg.Column.
func dedupeColumnPaths(a, b []ColumnPath) []ColumnPath {
	seen := map[string]bool{}
	var out []ColumnPath
	for _, c := range a {
		k := c.Key()
		if !seen[k] {
			seen[k] = true
			out = append(out, c)
		}
	}
	for _, c := range b {
		k := c.Key()
		if !seen[k] {
			seen[k] = true
			out = append(out, c)
		}
	}
	return out
}

// firstColumnNotIn returns the first of cols absent from exclude, nil if every column is excluded.
func firstColumnNotIn(cols []*pg.Column, exclude []*pg.Column) *pg.Column {
	ex := map[*pg.Column]bool{}
	for _, c := range exclude {
		ex[c] = true
	}
	for _, c := range cols {
		if !ex[c] {
			return c
		}
	}
	return nil
}

// excludeColumnPaths drops any cols entry matching exclude ; only plain columns match, identity is never composite.
func excludeColumnPaths(cols []ColumnPath, exclude []*pg.Column) []ColumnPath {
	ex := map[*pg.Column]bool{}
	for _, c := range exclude {
		ex[c] = true
	}
	var out []ColumnPath
	for _, c := range cols {
		if len(c.Path) == 1 && ex[c.Path[0]] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// writeTargetPath writes cp as an INSERT/UPDATE target : "col.field", not parenthesized like a read reference.
func writeTargetPath(w *writer.SQLWriter, cp ColumnPath) {
	for i, col := range cp.Path {
		if i > 0 {
			w.Write(".")
		}
		w.Id(col.Name)
	}
}

// ---- phase 2 : delete -----------------------------------------------------------------

// runDelete deletes node's rows absent from the payload, scoped to parent (nil at root) by node's FK-to-parent columns.
func (dc *dmlCompiler) runDelete(ctx context.Context, node *QueryNode, parent *QueryNode) error {
	identCols, err := requireIdentityColumns(node, "deleteonly/merge")
	if err != nil {
		return err
	}
	tt := dc.tempOf(node)

	w := writer.NewSQL()
	w.Write("delete from ")
	w.Write(node.Relation.Identifier.EscapedString())
	w.Write(" t\n")
	w.Write("where ")

	wroteCond := false
	if parent != nil {
		pt := dc.tempOf(parent)
		for _, jc := range node.JoinColumns {
			if wroteCond {
				w.Write(" and ")
			}
			// = any(array(...)), not in (select ...) : with no statistics on the temp table, the planner then scans the
			// whole target table instead of probing its index.
			w.Write("t.")
			w.Id(jc.Local.Name)
			w.Write(" = any (array(select (")
			w.Write(pt.colRef("p", jc.Distant))
			w.Write(")::")
			w.Write(jc.Local.Type.PgIdentifier.EscapedString())
			w.Write(" from ")
			w.Write(pt.qualifiedGeneral())
			w.Write(" p")
			writeWhere(w, dc.nodeCond(parent, "p"))
			w.Write("))")
			wroteCond = true
		}
	}
	if wroteCond {
		w.Write(" and ")
	}
	w.Write("(")
	for i, col := range identCols {
		if i > 0 {
			w.Write(", ")
		}
		w.Write("t.")
		w.Id(col.Name)
	}
	// The rows to keep are this node's rows that have an identity : an inserted row got its keys written
	// back, a matched one recovered them, a supplied identity names an existing row. A deleteonly node writes
	// nothing, so what it keeps is the identities its payload names.
	w.Write(") not in (select ")
	for i, col := range identCols {
		if i > 0 {
			w.Write(", ")
		}
		w.Write(tt.colRef("w", col))
	}
	w.Write(" from ")
	w.Write(tt.qualifiedGeneral())
	w.Write(" w")
	keep := dc.nodeCond(node, "w")
	for _, col := range identCols {
		keep = append(keep, tt.rawColRef("w", col)+" is not null")
	}
	writeWhere(w, keep)
	w.Write(")")

	// node.Where scopes every delete-bearing mode, or a partial payload would delete rows never selected.
	if node.Where != nil {
		w.Write(" and (")
		sc := &sqlCompiler{w: w, alias: map[*QueryNode]string{node: "t"}}
		if err := sc.compileExpr(node.Where, node); err != nil {
			return oops.Wrapf(err, "compiling where")
		}
		w.Write(")")
	}

	args, err := dc.args(w)
	if err != nil {
		return err
	}
	if err := dc.submit(ctx, node, "delete", w, args, func(tag pgconn.CommandTag) {
		if dc.collectStats {
			dc.stat(node).Deleted += int(tag.RowsAffected())
		}
	}); err != nil {
		return err
	}
	return nil
}

// ---- where-driven root modes ---------------------------------------------------------

// compileRootWhere writes node's where, aliasing the relation as "t".
func (dc *dmlCompiler) compileRootWhere(w *writer.SQLWriter, node *QueryNode) error {
	sc := &sqlCompiler{w: w, alias: map[*QueryNode]string{node: "t"}}
	w.Write("(")
	if err := sc.compileExpr(node.Where, node); err != nil {
		return oops.Wrapf(err, "compiling where")
	}
	w.Write(")")
	return nil
}

// runDeleteWhere : write_mode "delete" — removes every row node's where selects.
func (dc *dmlCompiler) runDeleteWhere(ctx context.Context, node *QueryNode) error {
	w := writer.NewSQL()
	w.Write("delete from ")
	w.Write(node.Relation.Identifier.EscapedString())
	w.Write(" t\nwhere ")
	if err := dc.compileRootWhere(w, node); err != nil {
		return err
	}
	args, err := dc.args(w)
	if err != nil {
		return err
	}
	if err := dc.submit(ctx, node, "delete", w, args, func(tag pgconn.CommandTag) {
		if dc.collectStats {
			dc.stat(node).Deleted += int(tag.RowsAffected())
		}
	}); err != nil {
		return err
	}
	return nil
}

// patchSetColumns returns the writable columns the patch names, from the presence words of its row.
func patchSetColumns(node *QueryNode, mask []int64) ([]ColumnPath, error) {
	allowlist := node.UpdateColumns
	if len(allowlist) == 0 {
		allowlist = node.InsertColumns
	}
	cols, err := columnsFor(node, allowlist)
	if err != nil {
		return nil, err
	}
	var setCols []ColumnPath
	for _, cp := range cols {
		slot, ok := node.Relation.MaskSlotFor(cp.Path...)
		if ok && slot.Word < len(mask) && uint64(mask[slot.Word])&(uint64(1)<<slot.Bit) != 0 {
			setCols = append(setCols, cp)
		}
	}
	if len(setCols) == 0 {
		return nil, oops.Errorf("update: the patch names no column the select makes writable")
	}
	return setCols, nil
}

// runUpdateWhere : write_mode "update" — applies the patch in temp row dc.patchRowID to every row node's where
// selects, setting only the columns it names. Each updated row's keys land in a fresh temp row for the read-back.
func (dc *dmlCompiler) runUpdateWhere(ctx context.Context, node *QueryNode) error {
	if dc.fast != nil {
		return dc.runUpdateWhereFast(ctx, node)
	}
	if err := dc.flush(ctx); err != nil { // the patch row is loaded by a queued statement
		return err
	}
	setCols, err := patchSetColumns(node, dc.patchMask)
	if err != nil {
		return err
	}

	tt := dc.tempOf(node)
	relID := node.Relation.Identifier.EscapedString()
	keyCols := keysColumns(node)
	w := writer.NewSQL()
	w.Write("with p as (\n\tselect * from ")
	w.Write(tt.qualifiedGeneral())
	w.Write(" where ")
	w.Write(quoteIdent(tt.rowIDCol()))
	w.Write(" = ")
	w.Write(fmt.Sprintf("%d", dc.patchRowID))
	w.Write("\n),\nupd as (\n\tupdate ")
	w.Write(relID)
	w.Write(" t\n\tset ")
	for i, cp := range setCols {
		if i > 0 {
			w.Write(", ")
		}
		writeTargetPath(w, cp)
		w.Write(" = ")
		w.Write(tt.cpRef("p", cp.Path))
	}
	w.Write("\n\tfrom p\n\twhere ")
	if err := dc.compileRootWhere(w, node); err != nil {
		return err
	}
	w.Write("\n\treturning ")
	writeKeyList(w, node, "t")
	w.Write("\n),\ngone as (delete from ")
	w.Write(tt.qualifiedGeneral())
	w.Write(" where ")
	w.Write(quoteIdent(tt.rowIDCol()))
	w.Write(" = ")
	w.Write(fmt.Sprintf("%d", dc.patchRowID))
	w.Write(")\ninsert into ")
	w.Write(tt.qualifiedGeneral())
	w.Write(" (")
	w.Write(quoteIdent(tt.rowIDCol()) + ", " + quoteIdent(tt.nodeIDCol()))
	for _, col := range keyCols {
		w.Write(", ")
		w.Id(col.Name)
	}
	w.Write(")\nselect ")
	w.Write(fmt.Sprintf("%d", dc.nextRowID))
	w.Write(" + row_number() over (), ")
	w.Write(fmt.Sprintf("%d", dc.ids[node]))
	for _, col := range keyCols {
		w.Write(", upd.")
		w.Id(col.Name)
		if !tt.cols[tt.colIdx[col]].binary {
			w.Write("::text")
		}
	}
	w.Write(" from upd")

	args, err := dc.args(w)
	if err != nil {
		return err
	}
	if err := dc.submit(ctx, node, "update", w, args, func(tag pgconn.CommandTag) {
		if dc.collectStats {
			dc.stat(node).Updated += int(tag.RowsAffected())
		}
	}); err != nil {
		return err
	}
	// RowsAffected is zero under EXPLAIN ANALYZE, so ask the table itself.
	if err := dc.flush(ctx); err != nil {
		return err
	}
	var last int
	if err := queryOne(ctx, dc.conn, &last, fmt.Sprintf(`select coalesce(max(%s) filter (where %s >= $1), $1 - 1) from %s`,
		quoteIdent(tt.rowIDCol()), quoteIdent(tt.rowIDCol()), tt.qualifiedGeneral()), dc.nextRowID); err != nil {
		return oops.Wrapf(err, "advancing the row id")
	}
	dc.nextRowID = last + 1
	return nil
}

// queryOne runs a statement expected to return one row and scans its first column into dest.
func queryOne(ctx context.Context, conn Querier, dest any, sql string, args ...any) error {
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return pgx.ErrNoRows
	}
	return rows.Scan(dest)
}
