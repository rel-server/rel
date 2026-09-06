// Phase 1 (insert/update/upsert) and phase 2 (delete) DML, per
// specs/query-engine.md ## Writing Algorithm ### Implementation / ###
// Insertion-Updates. One dmlCompiler carries the shared state (connection,
// node-ID assignment) across the whole tree walk.
package query

import (
	"context"
	"fmt"

	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/writer"
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
		return fmt.Errorf("write: node %q: %w", node.InnerName, err)
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
				return fmt.Errorf("write: node %q: delete: %w", node.InnerName, err)
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
		return fmt.Errorf("unhandled write_mode %v", node.WriteMode)
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
	if _, err := dc.conn.Exec(ctx, w.String(), args...); err != nil {
		return fmt.Errorf("insert: %w\nsql: %s", err, w.String())
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
		return fmt.Errorf("recovering merge-new keys: %w\nsql: %s", err, w.String())
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
	identCols := identityColumns(node)
	if len(identCols) == 0 {
		return fmt.Errorf("update requires an identity (on_conflict or primary key) column set")
	}

	w := writer.NewSQL()
	w.Write("with ")
	if err := dc.writeResolvedCTE(w, node, dedupeColumnPaths(cols, wrapColumns(node, identCols))); err != nil {
		return err
	}
	w.Write("\n")
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

	args, err := dc.args(w)
	if err != nil {
		return err
	}
	rows, err := dc.conn.Query(ctx, w.String(), args...)
	if err != nil {
		return fmt.Errorf("update: %w\nsql: %s", err, w.String())
	}
	type kv struct {
		rowID int
		keys  []byte
	}
	var results []kv
	for rows.Next() {
		var rowID int
		var keys []byte
		if err := rows.Scan(&rowID, &keys); err != nil {
			rows.Close()
			return err
		}
		results = append(results, kv{rowID, keys})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range results {
		if _, err := dc.conn.Exec(ctx, `update _data set keys = $1 where __row_id = $2`, r.keys, r.rowID); err != nil {
			return fmt.Errorf("writing back update keys: %w", err)
		}
	}
	return nil
}

// runUpsert : insert ... on conflict (...) do update ... returning,
// correlated back via on_conflict columns (known pre-insert, unlike a generated PK).
func (dc *dmlCompiler) runUpsert(ctx context.Context, node *QueryNode) error {
	identCols := identityColumns(node)
	if len(identCols) == 0 {
		return fmt.Errorf("upsert requires an identity (on_conflict or primary key) column set")
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
	// the conflict target.
	updateCols = excludeColumnPaths(updateCols, identCols)

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
		w.Write("update set ")
		w.Id(identCols[0].Name)
		w.Write(" = excluded.")
		w.Id(identCols[0].Name)
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
	w.Unindent()
	w.Write("\n)\n")
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

	args, err := dc.args(w)
	if err != nil {
		return err
	}
	if _, err := dc.conn.Exec(ctx, w.String(), args...); err != nil {
		return fmt.Errorf("upsert: %w\nsql: %s", err, w.String())
	}
	return nil
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
	identCols := identityColumns(node)
	if len(identCols) == 0 {
		return fmt.Errorf("deleteonly/merge requires an identity (on_conflict or primary key) column set")
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
			return fmt.Errorf("compiling where: %w", err)
		}
		w.Write(")")
	}

	args, err := dc.args(w)
	if err != nil {
		return err
	}
	if _, err := dc.conn.Exec(ctx, w.String(), args...); err != nil {
		return fmt.Errorf("delete: %w\nsql: %s", err, w.String())
	}
	return nil
}
