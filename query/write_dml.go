// Phase 1 (insert/update/upsert) and phase 2 (delete) DML, per
// specs/query-engine.md ## Writing Algorithm ### Implementation / ###
// Insertion-Updates. One dmlCompiler carries the shared state (connection,
// node-ID assignment) across the whole tree walk.
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

	// Resolves a well-known write's $param references against this
	// request's params ; nil for a plain /rel write (never has a ParamExpr).
	paramValues map[string]any

	// temps holds the temp table of every relation the request writes (write_temp.go).
	temps map[*pg.Relation]*tempTable

	// tableNodes is, per relation, the node ids that have rows in its temp table so far in the request.
	tableNodes map[*pg.Relation]map[int]bool

	// Node IDs that received a row from denormalize ; absent means
	// unpopulated (## Writing Algorithm's noop-skipping note).
	populated map[int]bool

	// collectStats/submitted/statsByNode/statOrder back specs/complex-query.md
	// ## stats ; submitted is keyed by __node_id, precomputed once in
	// write.go from denormalize's own row output. statOrder preserves
	// first-touch order across phase1/phase2, since a Stat is one entry per
	// node even when both phases touch it (e.g. MERGE's upsert then delete).
	collectStats bool
	submitted    map[int]int
	statsByNode  map[*QueryNode]*Stat
	statOrder    []*QueryNode

	// collectSQL/sqlByNode/sqlOrder back specs/complex-query.md ## sql :
	// every DML statement's already-compiled text, collected instead of
	// only executed — never an extra round trip.
	collectSQL bool
	sqlByNode  map[*QueryNode]*SqlResult
	sqlOrder   []*QueryNode

	// explainAnalyze/planByNode/planOrder back specs/complex-query.md ##
	// query_plan : true wraps every DML statement in EXPLAIN (ANALYZE,
	// FORMAT JSON), executed for real, instead of running it plainly.
	// Mutually exclusive with collectStats — server/rel.go's own job to
	// reject that combination before calling in.
	explainAnalyze bool
	planByNode     map[*QueryNode]*PlanResult
	planOrder      []*QueryNode

	// batcher, when the connection can pipeline, makes run* queue their statements
	// in queue instead of sending each one ; flush sends them all in one round trip.
	batcher Batcher
	queue   []queuedStmt

	// format is how a fast write's response rows are produced (WriteOptions.Format).
	format OutputFormat

	// fast, when set, makes run* write straight from the request's rows and answer from
	// the write statement's own RETURNING rows, without the general temp table (query/write_fast.go).
	fast *fastWrite

	// nextRowID is the first free row id ; runUpdateWhere allocates
	// one row per row it updates, past the ids denormalize used.
	nextRowID int
	// patchRowID is the row holding an "update" root's patch, and patchMask its presence words.
	patchRowID int
	patchMask  []int64
}

// tempOf returns the general temp table of node's relation.
func (dc *dmlCompiler) tempOf(node *QueryNode) *tempTable {
	return dc.temps[node.Relation]
}

// soleNode reports whether every row in node's temp table belongs to node : a statement then needs no
// "node_id = N" filter, and is better off without it, since the planner has no statistics on a temp table
// and takes that equality to keep 0.5% of the rows, which turns a join of two thousand-row tables into a
// million-comparison nested loop.
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

// exec is every run*'s single execution point for its main DML statement
// (never temp-table housekeeping, which stays a bare dc.conn.Exec — same
// exclusion ## stats already draws) : records kind's compiled text under
// node when collectSQL, and — mutually exclusively with collectStats —
// substitutes EXPLAIN (ANALYZE, FORMAT JSON) for the statement itself when
// explainAnalyze, since the Writing Algorithm's later statements depend on
// this one's real side effects having already happened (specs/complex-query.md
// ## query_plan).
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

// submit is exec for a statement whose outcome nothing later in Go depends on
// beyond its command tag : with a Batcher it is queued, otherwise (or under
// EXPLAIN ANALYZE, which reads rows back) it runs now. after runs once the
// statement has run, before submit's caller's later statements are sent.
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

// flush sends every queued statement in one round trip, in order, and runs
// their after callbacks. The first failure stops everything after it, as in a
// transaction, and is reported with the statement that caused it.
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

// runExplainAnalyze runs sql (with args) wrapped in EXPLAIN (ANALYZE, FORMAT
// JSON) — for real, side effects included — and returns the single JSON
// value Postgres reports (an array holding one plan object).
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

// nodeStatPath is the chain of join-alias keys from root down to node — []
// for the root itself (specs/complex-query.md ## stats's Stat.path).
func nodeStatPath(node *QueryNode) []string {
	path := []string{}
	for n := node; n.Parent != nil; n = n.Parent {
		path = append([]string{n.OuterAlias}, path...)
	}
	return path
}

// stat get-or-creates node's Stat entry — called only when collectStats,
// since it's also what marks a node as "touched" for stats purposes.
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

// finalStats orders statsByNode by first-touch (statOrder) into the slice
// WriteResult.Stats reports ; nil when collectStats was never set.
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

// phase1 : outgoing children, then this node's own DML, then incoming
// children (step 3) ; the whole subtree skips when unpopulated (a no-op).
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

// phase2 : incoming children deepest-first, then this node's own delete
// (step 4). Delete gates on the PARENT's population — an absent subtree IS "delete everything here", never a short-circuit.
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

// args resolves w's bind slots against dc.paramValues — the one place
// every run* function turns a statement into its (sql, args) pair.
func (dc *dmlCompiler) args(w *writer.SQLWriter) ([]any, error) {
	return w.ResolveArgs(dc.paramValues)
}

// ---- shared column-set helpers -----------------------------------------------------

// identityColumns is on_conflict's columns if set, else the primary key —
// the same selection shape.go's identityIsWritable already applies.
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

// requireIdentityColumns is identityColumns, rejecting the empty result
// runUpdate/runUpsert/runDelete all need an identity set for (op names the
// operation in the resulting error, e.g. "update" or "deleteonly/merge").
func requireIdentityColumns(node *QueryNode, op string) ([]*pg.Column, error) {
	identCols := identityColumns(node)
	if len(identCols) == 0 {
		return nil, oops.With("relation", node.Relation.Identifier.String()).Errorf("%s requires an identity (on_conflict or primary key) column set", op)
	}
	return identCols, nil
}

// columnsFor applies an insert_columns/update_columns allowlist (empty = no
// filtering) to node.Shape.Extractors ; ColumnPath since an Extractor may be a composite sub-field.
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
	// Outgoing-child FK columns are structural, not payload content — never
	// in Shape.Extractors, so they bypass "allowed" like identity columns do.
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

// wrapColumns wraps plain identity/key columns as single-segment
// ColumnPaths, to merge with columnsFor's own []ColumnPath output.
func wrapColumns(node *QueryNode, cols []*pg.Column) []ColumnPath {
	out := make([]ColumnPath, len(cols))
	for i, c := range cols {
		out[i] = ColumnPath{Node: node, Path: []*pg.Column{c}}
	}
	return out
}

// withKeysColumns unions cols with keysColumns(node) ; runUpdate reads
// keys off "t" (RETURNING) instead, so skips this.
func withKeysColumns(node *QueryNode, cols []ColumnPath) []ColumnPath {
	return dedupeColumnPaths(cols, wrapColumns(node, keysColumns(node)))
}

// outgoingKeySource finds the outgoing child whose recovered key supplies
// node's col (### Insertion/Updates' outgoing "on" mapping, parent's side).
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

// incomingKeySource reports whether col is node's own FK column pointing
// at its literal JSON-tree parent (node ∈ parent.IncomingNodes).
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

// writeResolvedCTE writes "resolved as (select ...)" (no leading "with"),
// one row per temp row, per ### Insertion/Updates' three-way rule. withMasks
// also exposes the row's presence words as "__m0", "__m1", ..., for statements
// that must tell an absent column from a supplied one (runUpdate).
func (dc *dmlCompiler) writeResolvedCTE(w *writer.SQLWriter, node *QueryNode, cols []ColumnPath, withMasks bool) error {
	t := dc.tempOf(node)

	needsPar := false
	outgoingAliases := map[*QueryNode]string{}
	var outgoingOrder []*QueryNode // deterministic emission order — map iteration order isn't
	for _, cp := range cols {
		if len(cp.Path) != 1 {
			continue // a composite sub-field is never FK-linkage — see writeColumnCase
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

// writeColumnCase writes the 3-way per-column resolution rule for a plain
// column ; a composite sub-field (len(cp.Path)>1) always reads its field of the temp row's composite
// value instead — never FK-linkage or default-backed.
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

// isKeyLinked reports whether col gets its value from a parent's or an outgoing
// child's recovered key rather than from the payload (see writeColumnCase).
func isKeyLinked(node *QueryNode, col *pg.Column) bool {
	if _, ok := incomingKeySource(node, col); ok {
		return true
	}
	child, _ := outgoingKeySource(node, col)
	return child != nil
}

// keysColumns is identityColumns(node) plus every column an incoming
// child's JoinColumns needs — not necessarily the same set as on_conflict's target.
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
	// Symmetric case : as an outgoing child, node's own jc.Local may not
	// already be in identityColumns(node) (a join may target any unique column).
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

// runInsert : plain insert, or with doNothing (MERGE_NEW) an "on conflict
// do nothing" plus a separate recovery select for pre-existing rows' keys.
func (dc *dmlCompiler) runInsert(ctx context.Context, node *QueryNode, doNothing bool) error {
	cols, err := columnsFor(node, node.InsertColumns)
	if err != nil {
		return err
	}
	// Every keysColumns member must be forced into both the resolved CTE
	// and the INSERT column list, or a member absent from cols is a flat SQL error (or a discarded, mismatched sequence value).
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
			// The trailing key write-back only ever joins actually-inserted
			// rows (via "ins" in the doNothing branch, or unconditionally
			// otherwise) — its RowsAffected() is exactly the insert count.
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

// runUpdate : a single update ... from ... returning suffices, since the
// row's identity is already known before the statement runs.
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
	// The identity columns (on_conflict, else the primary key) locate the row, so
	// they are never SET : "generated always" identity rejects even an unchanged
	// value. A primary key that is not the matching key can be changed.
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
		// Nothing to change (select holds only identity columns, e.g. a parent
		// kept in place for its nested children) : read the row's keys instead.
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
		// Keys read off "t" (post-update row via RETURNING), unlike runInsert :
		// no need to force keysColumns' extras into the SET list here.
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
	// Folded into "dml" itself (like runUpsert), not a separate per-row Go
	// round trip : keeps this EXPLAIN-ANALYZE-safe for query_plan, since
	// nothing downstream needs RETURNING back in Go (specs/complex-query.md
	// ## query_plan).
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

// runUpsert : insert ... on conflict (...) do update ... returning,
// correlated back via on_conflict columns (known pre-insert, unlike a generated PK).
func (dc *dmlCompiler) runUpsert(ctx context.Context, node *QueryNode) error {
	identCols, err := requireIdentityColumns(node, "upsert")
	if err != nil {
		return err
	}
	insertCols, err := columnsFor(node, node.InsertColumns)
	if err != nil {
		return err
	}
	// Same reasoning as runInsert : "dml"'s RETURNING needs every
	// keysColumns member selectable, and the insert side must write it.
	insertCols = withKeysColumns(node, insertCols)
	updateAllowlist := node.UpdateColumns
	if len(updateAllowlist) == 0 {
		updateAllowlist = node.InsertColumns
	}
	updateCols, err := columnsFor(node, updateAllowlist)
	if err != nil {
		return err
	}
	// Never update the identity columns themselves via excluded.* — they're
	// the conflict target. The primary key is excluded too even when
	// on_conflict names a different constraint : excluded.<pk> is a phantom
	// pre-insert value (nextval() for a generated column) that must never
	// overwrite a matched row's real, pre-existing identity.
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
		// Nothing to update ; still need a no-op update so RETURNING sees
		// the conflicting row (ON CONFLICT DO NOTHING has no RETURNING).
		// identCols[0] itself may be the relation's real, generated-always
		// PK (the default on_conflict target) — Postgres rejects any SET on
		// such a column besides the literal DEFAULT keyword, which would
		// regenerate it. Self-reference some other real column by the
		// target table's own name instead (not "excluded", whose value for
		// an identity column is only a phantom pre-insert placeholder).
		// Only the real PK is off-limits here — unlike updateCols above, a
		// non-PK on_conflict column (identCols) is perfectly safe to
		// self-reference.
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
			// "excluded" is a row, not a flat CTE : needs row-value
			// parenthesization for a composite sub-field, verified against PG 16.
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
	// "one CommandTag can't tell an insert from an update on its own" —
	// specs/complex-query.md ## stats. Always present (negligible cost) ;
	// only consumed by the collectStats branch below.
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

	// collectStats : the temp-row key write-back becomes its own CTE ("upd"), so
	// the outermost statement can be a plain SELECT reading dml's __inserted
	// split — a data-modifying CTE only executes when referenced downstream,
	// so "upd" is force-referenced via a scalar subquery in that SELECT.
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

// writeInsertKeyWriteBack writes the key write-back shared by
// runInsert's doNothing and plain-insert branches — identical shape, only
// the source of each row's keys differs : "ins" joined by identity
// columns (doNothing, since "ins" only RETURNs actually-inserted rows —
// recoverKeys handles the rest) vs. "resolved" directly (plain insert,
// every row was just inserted).
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

// writeUpsertKeyWriteBack writes the key write-back shared by
// runUpsert's stats and non-stats tails — identical either way, only
// where it's embedded (a bare statement vs. a "upd as (...)" CTE) differs.
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

// dedupeColumnPaths dedupes by ColumnPath.Key(), not the terminal *pg.Column
// pointer : two different composite sub-fields can share that same leaf pointer.
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

// firstColumnNotIn returns the first of cols absent from exclude, nil if
// every column is excluded.
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

// excludeColumnPaths drops any cols entry matching exclude ; only
// len(Path)==1 can match, since identity columns are never composite.
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

// writeTargetPath emits cp as an INSERT/UPDATE target — "col.field", not
// parenthesized like a read reference (verified against Postgres 16).
func writeTargetPath(w *writer.SQLWriter, cp ColumnPath) {
	for i, col := range cp.Path {
		if i > 0 {
			w.Write(".")
		}
		w.Id(col.Name)
	}
}

// ---- phase 2 : delete -----------------------------------------------------------------

// runDelete deletes node's rows absent from the payload, scoped to parent
// (nil at root) via node's own FK-to-parent columns (### Definitions).
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
			// = any(array(...)) rather than in (select ...) : the planner has no statistics on the temp
			// table and takes a one-row table for a full page of rows, which makes it scan the whole
			// target table instead of probing its index ; an array it assumes small.
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

	// node.Where must scope every delete-bearing mode too (query.ts's
	// write_mode doc), or a partial payload deletes rows never selected.
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

// runUpdateWhere : write_mode "update" — applies the patch held in the temp row
// dc.patchRowID to every row node's where selects, setting only the
// columns the patch names. Each updated row's keys land in a fresh temp row,
// which is what the read-back scopes by (like any other write).
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
