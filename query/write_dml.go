// Phase 1 (insert/update/upsert) and phase 2 (delete) DML, per
// specs/querying.md ## Writing Algorithm ### Implementation / ###
// Insertion-Updates. One dmlCompiler carries the shared state (connection,
// node-ID assignment) across the whole tree walk.
package query

import (
	"context"
	"fmt"
	"slices"

	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/writer"
)

type dmlCompiler struct {
	conn Querier
	ids  map[*QueryNode]int
}

// ---- traversal --------------------------------------------------------------------

// phase1 : outgoing children first (this node's own dependencies), then this
// node's own DML, then incoming children — spec step 3.
func (dc *dmlCompiler) phase1(ctx context.Context, node *QueryNode) error {
	if _, ok := dc.ids[node]; !ok {
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

// phase2 : incoming children deepest-first, then outgoing children walked
// through (never emitting their own delete), then this node's own delete if
// it has a delete component — spec step 4. parent is nil at the root.
func (dc *dmlCompiler) phase2(ctx context.Context, node *QueryNode, parent *QueryNode) error {
	if _, ok := dc.ids[node]; !ok {
		return nil
	}
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
	if hasDeleteComponent(node.WriteMode) {
		if err := dc.runDelete(ctx, node, parent); err != nil {
			return fmt.Errorf("write: node %q: delete: %w", node.InnerName, err)
		}
	}
	return nil
}

// ---- shared column-set helpers -----------------------------------------------------

// identityColumns is the column set that identifies a row for key-recovery
// purposes : on_conflict's columns if set, else the primary key — the same
// selection shape.go's identityIsWritable already applies, not reimplemented
// independently.
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

// filterColumns applies an insert_columns/update_columns allowlist (empty =
// no filtering, per query.ts's doc comment) to node.Shape.Extractors, then
// forces in any identity column that's sequence/identity-backed (Default
// Expression set) even if it wasn't itself present in the payload — those
// need to be part of the "resolved" CTE regardless, so their pre-computed
// value (not Postgres's own insert-time default) is what both the physical
// INSERT and the recovered "keys" agree on. An identity column with no
// default at all must already come from the payload (pass 2's writability
// check already requires it), so no forcing is needed there.
func columnsFor(node *QueryNode, allowlist []string) ([]*pg.Column, error) {
	var cols []*pg.Column
	seen := map[*pg.Column]bool{}
	var allowed map[string]bool
	if len(allowlist) > 0 {
		allowed = make(map[string]bool, len(allowlist))
		for _, n := range allowlist {
			allowed[n] = true
		}
	}
	for _, ex := range node.Shape.Extractors {
		if len(ex.Path.Path) != 1 {
			return nil, fmt.Errorf("writing a composite sub-field (%v) isn't supported yet", ex.JsonPath)
		}
		col := ex.Path.Path[0]
		if allowed != nil && !allowed[col.Name] {
			continue
		}
		if !seen[col] {
			seen[col] = true
			cols = append(cols, col)
		}
	}
	for _, col := range identityColumns(node) {
		if !seen[col] && col.DefaultExpression != "" {
			seen[col] = true
			cols = append(cols, col)
		}
	}
	// Outgoing-child FK columns (e.g. movie.director_id, resolved from the
	// director child's own recovered key) are structural, not payload
	// content — they don't appear in Shape.Extractors at all (they're not
	// part of node's own select), and insert_columns/update_columns filters
	// a payload column allowlist, not this structural relationship, so they
	// bypass "allowed" the same way default-backed identity columns do.
	for _, c := range node.OutgoingNodes {
		for _, jc := range c.JoinColumns {
			if !seen[jc.Distant] {
				seen[jc.Distant] = true
				cols = append(cols, jc.Distant)
			}
		}
	}
	return cols, nil
}

// withKeysColumns unions cols with keysColumns(node) — needed wherever
// writeKeysObject's source is "resolved"/"dml" rather than a live
// post-write row (runInsert, runUpsert) : those read every recovered
// column straight off "resolved", so a keysColumns member not already
// among cols (e.g. profile.id when on_conflict is user_email — id is
// otherwise never forced in, since identityColumns(node) returns ONLY the
// on_conflict set) would be a flat SQL error ("column resolved.id does not
// exist"), not just a missing key. runUpdate deliberately does NOT use
// this : it reads keys off "t" (the live, post-update row via RETURNING)
// instead, so forcing extra columns into its SET list — which cols also
// drives there — would wrongly self-assign them.
func withKeysColumns(node *QueryNode, cols []*pg.Column) []*pg.Column {
	return dedupeColumns(cols, keysColumns(node))
}

// outgoingKeySource finds, for physical column col of node, the outgoing
// child (if any) whose already-recovered key supplies col's value — the
// "outgoing join's on mapping" case in ### Insertion/Updates, applied from
// the PARENT's side : an outgoing child C's own JoinColumns pairs C's own
// unique column (Local) with the column on ITS PARENT (Distant) that holds
// the FK value — so from node's (the parent's) perspective, col == Distant
// means node's own physical column is derived from C's Local key.
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

// incomingKeySource reports whether col is node's own FK column pointing at
// its literal JSON-tree parent (node ∈ parent.IncomingNodes case) — node's
// own JoinColumns pairs node's own FK column (Local) with the parent's
// referenced column (Distant).
func incomingKeySource(node *QueryNode, col *pg.Column) (keyCol *pg.Column, ok bool) {
	if node.Parent == nil {
		return nil, false
	}
	if !slices.Contains(node.Parent.IncomingNodes, node) {
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

// writeResolvedCTE writes "resolved as (select ...)" (without the leading
// "with" — callers combine it with other CTEs) : one row per _data row for
// node, one column per cols, using the three-way rule from
// ### Insertion/Updates. outgoingAliases records the join alias assigned to
// each outgoing child actually referenced by an emitted column, so the
// caller doesn't need to re-derive it.
func (dc *dmlCompiler) writeResolvedCTE(w *writer.SQLWriter, node *QueryNode, cols []*pg.Column) error {
	nodeID := dc.ids[node]
	relID := node.Relation.Identifier.EscapedString()

	needsPar := false
	outgoingAliases := map[*QueryNode]string{}
	var outgoingOrder []*QueryNode // deterministic emission order — map iteration order isn't
	for _, col := range cols {
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
	for _, col := range cols {
		w.Write(",\n")
		if err := dc.writeColumnCase(w, node, col, outgoingAliases); err != nil {
			return err
		}
		w.Write(" as ")
		w.Id(col.Name)
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

// writeColumnCase writes the 3-way per-column resolution rule.
func (dc *dmlCompiler) writeColumnCase(w *writer.SQLWriter, node *QueryNode, col *pg.Column, outgoingAliases map[*QueryNode]string) error {
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

// keysColumns is identityColumns(node) unioned with every column an
// incoming child's own JoinColumns needs from this node — the on_conflict
// target and "keys"'s content aren't necessarily the same set : on_conflict
// (e.g. profile's "user_email") only has to identify the conflicting row,
// but a child correlating back via a DIFFERENT column of node (typically
// the primary key, e.g. profile.id even though on_conflict is user_email)
// needs THAT column recovered into keys too, or incomingKeySource's
// "par.keys->>'id'" reads NULL. identityColumns(node) alone is what the
// conflict target / WHERE-correlation clauses need — only keys' own
// content needs this wider set.
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
	// Symmetric case : when node is itself an OUTGOING child of its own
	// parent, outgoingKeySource reads "par.keys->>'<jc.Local.Name>'" from
	// the PARENT's resolved CTE — jc.Local is a column of node, not
	// node.Parent, and per query.ts's own "on" doc a to-one join may target
	// any unique column, not just the primary key, so it isn't necessarily
	// covered by identityColumns(node) already.
	if node.Parent != nil && slices.Contains(node.Parent.OutgoingNodes, node) {
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
// reading each from source (e.g. "resolved" or "excluded"/the insert's own
// target-table alias).
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

// runInsert : plain insert (query.ts INSERT), or, when doNothing is true
// (MERGE_NEW), insert ... on conflict (identity) do nothing — followed by a
// separate recovery select for keys of rows that already existed and so
// contributed no RETURNING/resolved row of their own conflict outcome.
func (dc *dmlCompiler) runInsert(ctx context.Context, node *QueryNode, doNothing bool) error {
	cols, err := columnsFor(node, node.InsertColumns)
	if err != nil {
		return err
	}
	// writeKeysObject (below) reads from "resolved" for a plain insert (no
	// RETURNING available cross-table — see the package doc) ; force every
	// keysColumns member into both the resolved CTE and the physical INSERT
	// column list, or a member not already in cols (e.g. profile.id when
	// on_conflict is user_email) is a flat SQL error, and one that DOES have
	// a default (e.g. that same id) would otherwise get inserted via
	// Postgres's own nextval() rather than the CTE's pre-computed value —
	// two different sequence values, one silently discarded.
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
	for i, col := range cols {
		if i > 0 {
			w.Write(", ")
		}
		w.Id(col.Name)
	}
	w.Write(")\n")
	w.Write("overriding system value\n")
	w.Write("select ")
	for i, col := range cols {
		if i > 0 {
			w.Write(", ")
		}
		w.Write("resolved.")
		w.Id(col.Name)
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
		// "ins" only RETURNs a row for one that was ACTUALLY inserted — a
		// conflicting row, discarded by DO NOTHING, contributes nothing
		// here, so joining resolved to ins on the identity columns picks
		// out exactly the genuinely-new rows (never a phantom nextval()
		// value for a row that was never inserted). recoverKeys (below)
		// separately handles the conflicting remainder, which this join
		// can't see at all.
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

	if _, err := dc.conn.Exec(ctx, w.String(), w.Args()...); err != nil {
		return fmt.Errorf("insert: %w\nsql: %s", err, w.String())
	}

	if doNothing {
		return dc.recoverKeys(ctx, node)
	}
	return nil
}

// recoverKeys handles MERGE_NEW's "do nothing" branch : fills keys for rows
// runInsert's own join couldn't see — a conflicting row, which "ins" never
// RETURNs — by matching this node's "_data" rows against the real target
// table by identity (the on_conflict columns, always payload-supplied
// verbatim per pass 2's writability rule). Gated on "keys is null" : a
// genuinely-new row already got its (correct, non-phantom) keys from
// runInsert's own join against "ins", and must not be touched again here.
func (dc *dmlCompiler) recoverKeys(ctx context.Context, node *QueryNode) error {
	identCols := identityColumns(node)
	if len(identCols) == 0 {
		return nil
	}
	// Identity columns are read straight off _data.data with a jsonb arrow
	// rather than jsonb_populate_record, to avoid a LATERAL reference from
	// one FROM-list item into the UPDATE target table's own column
	// (_data.data), which Postgres rejects outside of an explicit LATERAL
	// join.
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

	if _, err := dc.conn.Exec(ctx, w.String(), w.Args()...); err != nil {
		return fmt.Errorf("recovering merge-new keys: %w\nsql: %s", err, w.String())
	}
	return nil
}

// runUpdate : query.ts UPDATE/MERGE_UPDATE — a single update ... from ...
// returning suffices, since the row's identity is already known before the
// statement runs.
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
	if err := dc.writeResolvedCTE(w, node, dedupeColumns(cols, identCols)); err != nil {
		return err
	}
	w.Write("\n")
	w.Write("update ")
	w.Write(node.Relation.Identifier.EscapedString())
	w.Write(" t\n")
	w.Write("set ")
	for i, col := range cols {
		if i > 0 {
			w.Write(", ")
		}
		w.Id(col.Name)
		w.Write(" = resolved.")
		w.Id(col.Name)
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
	// keys read off "t" (the live, post-update row, via RETURNING) rather
	// than "resolved" : unlike runInsert's plain-insert case, UPDATE's
	// RETURNING can freely reference the physical target table's own
	// columns, so there's no need to force keysColumns' extra members
	// (e.g. profile.id when on_conflict is user_email) into "resolved"/the
	// SET list at all — forcing them there would wrongly self-assign a
	// column update_columns never asked to touch.
	w.Write("returning resolved.__row_id, ")
	writeKeysObject(w, node, "t")
	w.Write(" as keys")

	rows, err := dc.conn.Query(ctx, w.String(), w.Args()...)
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

// runUpsert : query.ts UPSERT/MERGE — insert ... on conflict (...) do
// update ... returning, correlated back to _data via the on_conflict
// columns (known pre-insert, unlike a generated PK).
func (dc *dmlCompiler) runUpsert(ctx context.Context, node *QueryNode) error {
	identCols := identityColumns(node)
	if len(identCols) == 0 {
		return fmt.Errorf("upsert requires an identity (on_conflict or primary key) column set")
	}
	insertCols, err := columnsFor(node, node.InsertColumns)
	if err != nil {
		return err
	}
	// Same reasoning as runInsert : "dml"'s RETURNING (below) needs every
	// keysColumns member selectable, and the insert side needs to actually
	// write any default-backed one (e.g. profile.id) rather than let
	// Postgres generate its own.
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
	// the conflict target, updating them is meaningless (and for a PK,
	// actively wrong).
	updateCols = excludeColumns(updateCols, identCols)

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
	for i, col := range insertCols {
		if i > 0 {
			w.Write(", ")
		}
		w.Id(col.Name)
	}
	w.Write(")\n")
	w.Write("overriding system value\n")
	w.Write("select ")
	for i, col := range insertCols {
		if i > 0 {
			w.Write(", ")
		}
		w.Write("resolved.")
		w.Id(col.Name)
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
		for i, col := range updateCols {
			if i > 0 {
				w.Write(", ")
			}
			w.Id(col.Name)
			w.Write(" = excluded.")
			w.Id(col.Name)
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

	if _, err := dc.conn.Exec(ctx, w.String(), w.Args()...); err != nil {
		return fmt.Errorf("upsert: %w\nsql: %s", err, w.String())
	}
	return nil
}

func dedupeColumns(a, b []*pg.Column) []*pg.Column {
	seen := map[*pg.Column]bool{}
	var out []*pg.Column
	for _, c := range a {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	for _, c := range b {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

func excludeColumns(cols, exclude []*pg.Column) []*pg.Column {
	ex := map[*pg.Column]bool{}
	for _, c := range exclude {
		ex[c] = true
	}
	var out []*pg.Column
	for _, c := range cols {
		if !ex[c] {
			out = append(out, c)
		}
	}
	return out
}

// ---- phase 2 : delete -----------------------------------------------------------------

// runDelete deletes node's rows not present in this request's payload,
// scoped to parent (nil at the root). Per ### Definitions, a delete-bearing
// mode is only valid on an incoming (or root) relation, so node's own
// physical FK-to-parent columns (node.JoinColumns) are what scope the
// delete.
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

	// query.ts's write_mode doc comment is explicit, twice over : merge
	// "delete[s] rows not in the payload that match the where condition",
	// deleteonly "delete[s] only rows matching the where condition and not
	// in data" — node.Where must additionally scope every delete-bearing
	// mode, or an empty/partial payload deletes rows the query itself never
	// selected. Reuse compileExpr (sql_expr.go) against a throwaway
	// sqlCompiler with node pre-registered under alias "t" (this delete's
	// own target alias), same registration trick compileLateralJoin uses.
	if node.Where != nil {
		w.Write(" and (")
		sc := &sqlCompiler{w: w, alias: map[*QueryNode]string{node: "t"}}
		if err := sc.compileExpr(node.Where, node); err != nil {
			return fmt.Errorf("compiling where: %w", err)
		}
		w.Write(")")
	}

	if _, err := dc.conn.Exec(ctx, w.String(), w.Args()...); err != nil {
		return fmt.Errorf("delete: %w\nsql: %s", err, w.String())
	}
	return nil
}
