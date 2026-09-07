// Phase 1 (insert/update/upsert) and phase 2 (delete) DML, per
// specs/query-engine.md ## Writing Algorithm ### Implementation / ###
// Insertion-Updates. One dmlCompiler carries the shared state (connection,
// node-ID assignment) across the whole tree walk.
package query

import (
	"context"
	"encoding/json"
	"fmt"

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

	// Node IDs that received a "_data" row from denormalize ; absent means
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
// (never _data-only housekeeping, which stays a bare dc.conn.Exec — same
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
	var path []string
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
// one row per _data row, per ### Insertion/Updates' three-way rule.
func (dc *dmlCompiler) writeResolvedCTE(w *writer.SQLWriter, node *QueryNode, cols []ColumnPath) error {
	nodeID := dc.ids[node]
	relID := node.Relation.Identifier.EscapedString()

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
	w.Write("tmp.__row_id")
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
	w.Write("from _data tmp\n")
	w.Write("join lateral jsonb_populate_record(null::")
	w.Write(relID)
	w.Write(", tmp.data) as obj on true\n")
	if needsPar {
		w.Write("inner join _data par on par.__row_id = tmp.__parent_id\n")
	}
	for _, child := range outgoingOrder {
		alias := outgoingAliases[child]
		w.Write("left join _data ")
		w.Write(alias)
		w.Write(" on ")
		w.Write(alias)
		w.Write(".__node_id = ")
		w.Write(fmt.Sprintf("%d", dc.ids[child]))
		w.Write(" and ")
		w.Write(alias)
		w.Write(".__parent_id = tmp.__row_id\n")
	}
	w.Write("where tmp.__node_id = ")
	w.Write(fmt.Sprintf("%d", nodeID))
	w.Write("\n")
	w.Unindent()
	w.Write(")")
	return nil
}

// writeColumnCase writes the 3-way per-column resolution rule for a plain
// column ; a composite sub-field (len(cp.Path)>1) always reads tmp.data's flat key instead — never FK-linkage or default-backed.
func (dc *dmlCompiler) writeColumnCase(w *writer.SQLWriter, node *QueryNode, cp ColumnPath, outgoingAliases map[*QueryNode]string) error {
	if len(cp.Path) > 1 {
		leaf := cp.Path[len(cp.Path)-1]
		w.Write("(tmp.data->>")
		w.Bind(columnPathFlatName(cp))
		w.Write("::text)::")
		w.Write(leaf.Type.PgIdentifier.EscapedString())
		return nil
	}
	col := cp.Path[0]
	typeName := col.Type.PgIdentifier.EscapedString()

	if distant, ok := incomingKeySource(node, col); ok {
		w.Write("(par.keys->>")
		w.Bind(distant.Name)
		w.Write("::text)::")
		w.Write(typeName)
		return nil
	}
	if child, keyCol := outgoingKeySource(node, col); child != nil {
		alias := outgoingAliases[child]
		w.Write("(")
		w.Write(alias)
		w.Write(".keys->>")
		w.Bind(keyCol.Name)
		w.Write("::text)::")
		w.Write(typeName)
		return nil
	}

	if col.DefaultExpression != "" {
		w.Write("case when not (tmp.data ? ")
		w.Bind(col.Name)
		w.Write("::text) or (")
		if col.IsReallyNotNull() {
			w.Write("(tmp.data->>")
			w.Bind(col.Name)
			w.Write("::text) is null")
		} else {
			w.Write("false")
		}
		w.Write(") then ")
		w.Write(col.DefaultExpression)
		w.Write(" else obj.")
		w.Id(col.Name)
		w.Write(" end")
		return nil
	}

	w.Write("obj.")
	w.Id(col.Name)
	return nil
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

// writeKeysObject writes jsonb_build_object(...) over keysColumns(node),
// reading each from source ("resolved", or the insert's own alias).
func writeKeysObject(w *writer.SQLWriter, node *QueryNode, source string) {
	w.Write("jsonb_build_object(")
	for i, col := range keysColumns(node) {
		if i > 0 {
			w.Write(", ")
		}
		w.Bind(col.Name)
		w.Write("::text, ")
		w.Write(source)
		w.Write(".")
		w.Id(col.Name)
	}
	w.Write(")")
}

// ---- phase 1 dispatch ----------------------------------------------------------------

func (dc *dmlCompiler) runPhase1Node(ctx context.Context, node *QueryNode) error {
	switch node.WriteMode {
	case READONLY, DELETE_ONLY:
		return nil
	case INSERT, MERGE_NEW:
		return dc.runInsert(ctx, node, node.WriteMode == MERGE_NEW)
	case UPDATE, MERGE_UPDATE:
		return dc.runUpdate(ctx, node)
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
	w.Write("with ")
	if err := dc.writeResolvedCTE(w, node, cols); err != nil {
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
	w.Write(" from resolved\n")
	identCols := identityColumns(node)
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
	if doNothing {
		// "ins" only RETURNs actually-inserted rows ; joining on identity
		// columns picks out genuinely-new ones. recoverKeys handles the rest.
		w.Write("update _data\n")
		w.Write("set keys = r.keys\n")
		w.Write("from (select resolved.__row_id, ")
		writeKeysObject(w, node, "ins")
		w.Write(" as keys from resolved join ins on ")
		for i, col := range identCols {
			if i > 0 {
				w.Write(" and ")
			}
			w.Write("ins.")
			w.Id(col.Name)
			w.Write(" = resolved.")
			w.Id(col.Name)
		}
		w.Write(") r\n")
		w.Write("where _data.__row_id = r.__row_id")
	} else {
		w.Write("update _data\n")
		w.Write("set keys = r.keys\n")
		w.Write("from (select __row_id, ")
		writeKeysObject(w, node, "resolved")
		w.Write(" as keys from resolved) r\n")
		w.Write("where _data.__row_id = r.__row_id")
	}

	args, err := dc.args(w)
	if err != nil {
		return err
	}
	tag, err := dc.exec(ctx, node, "insert", w, args)
	if err != nil {
		return oops.With("sql", w.String()).Wrapf(err, "insert")
	}
	if dc.collectStats {
		// The trailing "update _data" only ever joins actually-inserted
		// rows (via "ins" in the doNothing branch, or unconditionally
		// otherwise) — its RowsAffected() is exactly the insert count.
		dc.stat(node).Inserted += int(tag.RowsAffected())
	}

	if doNothing {
		return dc.recoverKeys(ctx, node)
	}
	return nil
}

// recoverKeys handles MERGE_NEW's "do nothing" branch : fills keys for a
// conflicting row by identity match ; gated on "keys is null" so a new row isn't touched again.
func (dc *dmlCompiler) recoverKeys(ctx context.Context, node *QueryNode) error {
	identCols := identityColumns(node)
	if len(identCols) == 0 {
		return nil
	}
	// Read straight off _data.data with a jsonb arrow, not
	// jsonb_populate_record : Postgres rejects that LATERAL reference here.
	w := writer.NewSQL()
	w.Write("update _data\n")
	w.Write("set keys = ")
	writeKeysObject(w, node, "t")
	w.Write("\n")
	w.Write("from ")
	w.Write(node.Relation.Identifier.EscapedString())
	w.Write(" t\n")
	w.Write("where _data.__node_id = ")
	w.Write(fmt.Sprintf("%d", dc.ids[node]))
	w.Write(" and _data.keys is null and (")
	for i, col := range identCols {
		if i > 0 {
			w.Write(" and ")
		}
		w.Write("t.")
		w.Id(col.Name)
		w.Write(" = (_data.data->>")
		w.Bind(col.Name)
		w.Write("::text)::")
		w.Write(col.Type.PgIdentifier.EscapedString())
	}
	w.Write(")")

	args, err := dc.args(w)
	if err != nil {
		return err
	}
	if _, err := dc.conn.Exec(ctx, w.String(), args...); err != nil {
		return oops.With("sql", w.String()).Wrapf(err, "recovering merge-new keys")
	}
	return nil
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
	w.Write("with ")
	if err := dc.writeResolvedCTE(w, node, dedupeColumnPaths(cols, wrapColumns(node, identCols))); err != nil {
		return err
	}
	w.Write(",\ndml as (\n")
	w.Indent()
	w.Write("update ")
	w.Write(node.Relation.Identifier.EscapedString())
	w.Write(" t\n")
	w.Write("set ")
	for i, cp := range cols {
		if i > 0 {
			w.Write(", ")
		}
		writeTargetPath(w, cp)
		w.Write(" = resolved.")
		w.Id(columnPathFlatName(cp))
	}
	w.Write("\n")
	w.Write("from resolved\n")
	w.Write("where ")
	for i, col := range identCols {
		if i > 0 {
			w.Write(" and ")
		}
		w.Write("t.")
		w.Id(col.Name)
		w.Write(" = resolved.")
		w.Id(col.Name)
	}
	w.Write("\n")
	// Keys read off "t" (post-update row via RETURNING), unlike runInsert :
	// no need to force keysColumns' extras into the SET list here.
	w.Write("returning resolved.__row_id, ")
	writeKeysObject(w, node, "t")
	w.Write(" as keys")
	w.Unindent()
	w.Write("\n)\n")
	// Folded into "dml" itself (like runUpsert), not a separate per-row Go
	// round trip : keeps this EXPLAIN-ANALYZE-safe for query_plan, since
	// nothing downstream needs RETURNING back in Go (specs/complex-query.md
	// ## query_plan).
	w.Write("update _data\n")
	w.Write("set keys = dml.keys\n")
	w.Write("from dml\n")
	w.Write("where _data.__row_id = dml.__row_id")

	args, err := dc.args(w)
	if err != nil {
		return err
	}
	tag, err := dc.exec(ctx, node, "update", w, args)
	if err != nil {
		return oops.With("sql", w.String()).Wrapf(err, "update")
	}
	if dc.collectStats {
		dc.stat(node).Updated += int(tag.RowsAffected())
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
	w.Write("with ")
	if err := dc.writeResolvedCTE(w, node, insertCols); err != nil {
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
	w.Write(" from resolved\n")
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
		writeUpsertDataUpdate(w, node, identCols)
		args, err := dc.args(w)
		if err != nil {
			return err
		}
		if _, err := dc.exec(ctx, node, "upsert", w, args); err != nil {
			return oops.With("sql", w.String()).Wrapf(err, "upsert")
		}
		return nil
	}

	// collectStats : the "_data" write-back becomes its own CTE ("upd"), so
	// the outermost statement can be a plain SELECT reading dml's __inserted
	// split — a data-modifying CTE only executes when referenced downstream,
	// so "upd" is force-referenced via a scalar subquery in that SELECT.
	w.Write(",\nupd as (\n")
	w.Indent()
	writeUpsertDataUpdate(w, node, identCols)
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

// writeUpsertDataUpdate writes the "_data" keys write-back shared by
// runUpsert's stats and non-stats tails — identical either way, only where
// it's embedded (a bare statement vs. a "upd as (...)" CTE) differs.
func writeUpsertDataUpdate(w *writer.SQLWriter, node *QueryNode, identCols []*pg.Column) {
	w.Write("update _data\n")
	w.Write("set keys = ")
	writeKeysObject(w, node, "dml")
	w.Write("\n")
	w.Write("from resolved\n")
	w.Write("join dml on ")
	for i, col := range identCols {
		if i > 0 {
			w.Write(" and ")
		}
		w.Write("dml.")
		w.Id(col.Name)
		w.Write(" = resolved.")
		w.Id(col.Name)
	}
	w.Write("\n")
	w.Write("where _data.__row_id = resolved.__row_id")
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

	w := writer.NewSQL()
	w.Write("delete from ")
	w.Write(node.Relation.Identifier.EscapedString())
	w.Write(" t\n")
	w.Write("where ")

	wroteCond := false
	if parent != nil {
		for _, jc := range node.JoinColumns {
			if wroteCond {
				w.Write(" and ")
			}
			w.Write("t.")
			w.Id(jc.Local.Name)
			w.Write(" in (select (keys->>")
			w.Bind(jc.Distant.Name)
			w.Write("::text)::")
			w.Write(jc.Local.Type.PgIdentifier.EscapedString())
			w.Write(" from _data where __node_id = ")
			w.Write(fmt.Sprintf("%d", dc.ids[parent]))
			w.Write(")")
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
	w.Write(") not in (select ")
	for i, col := range identCols {
		if i > 0 {
			w.Write(", ")
		}
		w.Write("(keys->>")
		w.Bind(col.Name)
		w.Write("::text)::")
		w.Write(col.Type.PgIdentifier.EscapedString())
	}
	w.Write(" from _data where __node_id = ")
	w.Write(fmt.Sprintf("%d", dc.ids[node]))
	w.Write(" and keys is not null)")

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
	tag, err := dc.exec(ctx, node, "delete", w, args)
	if err != nil {
		return oops.With("sql", w.String()).Wrapf(err, "delete")
	}
	if dc.collectStats {
		dc.stat(node).Deleted += int(tag.RowsAffected())
	}
	return nil
}
