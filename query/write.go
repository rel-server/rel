// Pass 4, the Writing Algorithm : denormalizes a payload into typed temp tables, one per destination table, then
// executes phased DML off them. A temp table is created on the connection the first time a write touches its table,
// before the request's transaction begins ; its rows go away at commit or rollback.
package query

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/pg"
	"github.com/samber/oops"
)

// Querier is the connection surface writes need ; *pgxpool.Conn and *pgx.Conn satisfy it. Creating the temp tables
// also needs the underlying *pgx.Conn : a wrapper must expose it with a Conn() *pgx.Conn method.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error)
}

// WriteResult is a write's report ; the read-back builds the response from the temp tables.
type WriteResult struct {
	// NodeIDs is every writable node's __node_id ; READONLY nodes and their subtrees are absent.
	NodeIDs map[*QueryNode]int

	// RowCount is the number of temp rows this write produced, across every writable node.
	RowCount int

	// Rows is non-nil when the write built its own response : the JSON of each response row, in order (empty, non-nil
	// when there are none), and there is no read-back to run. Nil when the response has to be read back.
	Rows [][]byte

	// Stats is set only with WriteOptions.Stats : one entry per writable node a DML statement ran against.
	Stats []Stat

	// Sql is set only with WriteOptions.Sql : one entry per writable node touched, with each statement kind it ran.
	Sql []SqlResult

	// QueryPlan is set only with WriteOptions.QueryPlan : every DML statement ran under EXPLAIN (ANALYZE, FORMAT JSON).
	QueryPlan []PlanResult
}

// Stat is one writable node's row counts from a `stats` request.
type Stat struct {
	// Path is the chain of join-alias keys from the root down to this node ; [] for the root.
	Path      []string `json:"path"`
	Table     string   `json:"table"`     // fully-qualified relation name, "schema.table"
	Submitted int      `json:"submitted"` // rows denormalized for this node, written or not
	Inserted  int      `json:"inserted"`
	Updated   int      `json:"updated"`
	Deleted   int      `json:"deleted"`
}

// SqlResult is one node's statement texts from a `sql` request, one per statement kind it ran ; relexec fills Select
// (the read-back).
type SqlResult struct {
	Path   []string `json:"path"`
	Select string   `json:"select,omitempty"`
	Insert string   `json:"insert,omitempty"`
	Update string   `json:"update,omitempty"`
	Upsert string   `json:"upsert,omitempty"`
	Delete string   `json:"delete,omitempty"`
}

// PlanResult is one node's EXPLAIN (ANALYZE, FORMAT JSON) outputs from a `query_plan` request on a write ; relexec
// fills Select (the read-back).
type PlanResult struct {
	Path   []string        `json:"path"`
	Select json.RawMessage `json:"select,omitempty"`
	Insert json.RawMessage `json:"insert,omitempty"`
	Update json.RawMessage `json:"update,omitempty"`
	Upsert json.RawMessage `json:"upsert,omitempty"`
	Delete json.RawMessage `json:"delete,omitempty"`
}

// WriteOptions holds ComplexQuery's write-side response flags. QueryPlan and Stats are mutually exclusive, since
// EXPLAIN ANALYZE leaves no row counts to read ; the caller must reject the combination, it is not checked here.
type WriteOptions struct {
	Stats     bool
	QueryPlan bool
	Sql       bool
	// NoFastPath forces the general (temp table) path ; tests compare the two.
	NoFastPath bool

	// Format is how the fast path's WriteResult.Rows are produced : JSON text, or FormatCBOR records for a ResultEncoder.
	Format OutputFormat
}

// assignNodeIDs walks root pre-order, pruning a READONLY node's whole
// subtree ; startAt continues numbering across calls sharing one WriteState.
func assignNodeIDs(root *QueryNode, startAt int) (map[*QueryNode]int, int) {
	ids := map[*QueryNode]int{}
	next := startAt
	var walk func(n *QueryNode)
	walk = func(n *QueryNode) {
		if n.WriteMode == READONLY {
			return
		}
		ids[n] = next
		next++
		for _, c := range n.OutgoingNodes {
			walk(c)
		}
		for _, c := range n.IncomingNodes {
			walk(c)
		}
	}
	walk(root)
	return ids, next
}

// findUnwritableNode walks post-order for the deepest, originating
// non-writable node, root.Shape.Writable alone only names the root.
func findUnwritableNode(node *QueryNode) *QueryNode {
	if node.WriteMode == READONLY {
		return nil
	}
	for _, c := range node.OutgoingNodes {
		if found := findUnwritableNode(c); found != nil {
			return found
		}
	}
	for _, c := range node.IncomingNodes {
		if found := findUnwritableNode(c); found != nil {
			return found
		}
	}
	if node.Shape != nil && !node.Shape.Writable {
		return node
	}
	return nil
}

// unwritableNodeName picks InnerName, or the relation's identifier when a
// root has no alias of its own.
func unwritableNodeName(node *QueryNode) string {
	if node.InnerName != "" {
		return node.InnerName
	}
	if node.Relation != nil {
		return node.Relation.Identifier.String()
	}
	return "<root>"
}

// hasDeleteComponent reports whether m's semantics include deleting rows
// absent from the payload.
func hasDeleteComponent(m WriteMode) bool {
	switch m {
	case MERGE, MERGE_NEW, MERGE_UPDATE, DELETE_ONLY:
		return true
	default:
		return false
	}
}

// WriteState carries the node id and row id counters across the writes of one request, which share temp tables
// (the write items of a Sequence). They must all use the same WriteState, or they reuse ids and the statements
// joining temp tables by row id mix their rows up. The zero value is right for a request's first write.
type WriteState struct {
	nextNodeID int
	nextRowID  int

	// tableNodes is, per relation, the node ids that have rows loaded in its temp table so far.
	tableNodes map[*pg.Relation]map[int]bool
}

// ExecuteWriteNode runs the Writing Algorithm for one write item ; root must be Resolve'd. Missing temp tables are
// created on conn, which must then be outside a transaction (see PrepareWrite). payload is already decoded ; nil is
// none (what a "delete" takes), and it is never retained. state must be shared by every write of the request and is
// mutated in place. paramValues resolves a well-known write's $param references ; nil for a plain /rel write.
func ExecuteWriteNode(ctx context.Context, conn Querier, root *QueryNode, payload Node, state *WriteState, paramValues map[string]any, opts WriteOptions) (*WriteResult, error) {
	ids, nextNodeID := assignNodeIDs(root, state.nextNodeID)
	if _, ok := ids[root]; !ok {
		return nil, oops.Code(errcode.WriteForbidden).Errorf("write: root node is readonly, nothing to write")
	}
	if bad := findUnwritableNode(root); bad != nil {
		// A function-rooted node gets its own code.
		code := errcode.WriteForbidden
		if bad.IsFunction() {
			code = errcode.WriteForbiddenFunctionRoot
		}
		return nil, oops.With("relation", unwritableNodeName(bad)).Code(code).Errorf("write: relation %q is not writable — its identity columns must appear exactly once in the select output, untransformed and writable (docs/content/query-language/writing.md)", unwritableNodeName(bad))
	}

	if root.WriteMode == DELETE {
		return executeDeleteWhere(ctx, conn, root, ids, payload, paramValues, opts)
	}
	if root.WriteMode == UPDATE {
		wrapped, err := patchAsPayload(payload)
		if err != nil {
			return nil, err
		}
		payload = wrapped
	}
	ec := encodeContextFor(conn)
	if fastWriteEligible(root, opts) {
		return executeFast(ctx, conn, root, ids, payload, state, nextNodeID, paramValues, ec, opts.Format)
	}

	tables, err := prepareTables(ctx, conn, ids)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		return nil, oops.Errorf("write: payload must be an array of root rows")
	}
	rows, err := denormalizeNode(root, ids, payload, tables, state.nextRowID, true, ec)
	if err != nil {
		return nil, err
	}

	if state.tableNodes == nil {
		state.tableNodes = map[*pg.Relation]map[int]bool{}
	}
	for t, nodes := range rows.tableNodes {
		known := state.tableNodes[t.rel]
		if known == nil {
			known = map[int]bool{}
			state.tableNodes[t.rel] = known
		}
		for id := range nodes {
			known[id] = true
		}
	}

	dc := &dmlCompiler{
		conn: conn, ids: ids, temps: tables, tableNodes: state.tableNodes, populated: rows.populated, keyed: rows.keyed, omitted: rows.omitted, paramValues: paramValues, submitted: rows.submitted,
		collectStats: opts.Stats, collectSQL: opts.Sql, explainAnalyze: opts.QueryPlan,
		nextRowID: rows.nextRowID,
	}
	if b, ok := conn.(Batcher); ok {
		dc.batcher = b
	}
	if err := dc.loadRows(ctx, rows); err != nil {
		return nil, err
	}
	if root.WriteMode == UPDATE && rows.count > 0 {
		dc.patchRowID, dc.patchMask = int(rows.rootRowID), rows.patchMask
	}
	if err := dc.phase1(ctx, root); err != nil {
		return nil, err
	}
	if err := dc.phase2(ctx, root, nil); err != nil {
		return nil, err
	}
	if err := dc.flush(ctx); err != nil {
		return nil, err
	}

	state.nextNodeID = nextNodeID
	state.nextRowID = max(rows.nextRowID, dc.nextRowID)
	return &WriteResult{
		NodeIDs: ids, RowCount: rows.count,
		Stats: dc.finalStats(), Sql: dc.finalSQL(), QueryPlan: dc.finalPlans(),
	}, nil
}

// PrepareWrite creates on conn the temp tables the writes of roots go through, and registers their types ; tables
// already prepared are skipped. It must run outside a transaction, since a rollback would drop the tables.
func PrepareWrite(ctx context.Context, conn Querier, roots ...*QueryNode) error {
	rels := map[*pg.Relation]bool{}
	var walk func(n *QueryNode)
	walk = func(n *QueryNode) {
		if n.WriteMode == READONLY || n.Relation == nil || n.IsFunction() {
			return
		}
		if n.WriteMode != DELETE {
			rels[n.Relation] = true
		}
		for _, c := range n.OutgoingNodes {
			walk(c)
		}
		for _, c := range n.IncomingNodes {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return ensureAll(ctx, conn, rels)
}

// prepareTables ensures the temp tables of every writable node of a request and returns them by relation.
func prepareTables(ctx context.Context, conn Querier, ids map[*QueryNode]int) (map[*pg.Relation]*tempTable, error) {
	rels := map[*pg.Relation]bool{}
	for n := range ids {
		if n.Relation != nil {
			rels[n.Relation] = true
		}
	}
	return ensureAllTables(ctx, conn, rels)
}

func ensureAll(ctx context.Context, conn Querier, rels map[*pg.Relation]bool) error {
	_, err := ensureAllTables(ctx, conn, rels)
	return err
}

func ensureAllTables(ctx context.Context, conn Querier, rels map[*pg.Relation]bool) (map[*pg.Relation]*tempTable, error) {
	out := make(map[*pg.Relation]*tempTable, len(rels))
	ordered := make([]*pg.Relation, 0, len(rels))
	for r := range rels {
		ordered = append(ordered, r)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].PgRelId < ordered[j].PgRelId })
	for _, r := range ordered {
		t, err := ensureTempTables(ctx, conn, r)
		if err != nil {
			return nil, err
		}
		out[r] = t
	}
	return out, nil
}

// sessionZones caches the result of loading each session time zone name.
var sessionZones sync.Map // string -> zoneResult

type zoneResult struct {
	loc *time.Location
	err error
}

// encodeContextFor builds conn's encoding context from its session time zone (the TimeZone parameter).
func encodeContextFor(q Querier) *encodeContext {
	conn, ok := pgxConnOf(q)
	if !ok {
		return &encodeContext{}
	}
	name := conn.PgConn().ParameterStatus("TimeZone")
	if name == "" {
		return &encodeContext{}
	}
	if v, ok := sessionZones.Load(name); ok {
		r := v.(zoneResult)
		return &encodeContext{Location: r.loc, zoneName: name, zoneErr: r.err}
	}
	loc, err := time.LoadLocation(name)
	sessionZones.Store(name, zoneResult{loc, err})
	return &encodeContext{Location: loc, zoneName: name, zoneErr: err}
}

// patchAsPayload checks that an "update" root's data is one patch object and
// wraps it as the one-row array denormalize expects.
func patchAsPayload(payload Node) (Node, error) {
	if payload == nil || payload.Kind() != KindObject {
		return nil, oops.Code(errcode.WriteForbidden).Errorf(`write: write_mode "update" takes "data" as a single patch object`)
	}
	return &synthArray{items: []Node{payload}}, nil
}

// executeDeleteWhere runs write_mode "delete" : no payload, no read-back, one statement.
func executeDeleteWhere(ctx context.Context, conn Querier, root *QueryNode, ids map[*QueryNode]int, payload Node, paramValues map[string]any, opts WriteOptions) (*WriteResult, error) {
	if !noData(payload) {
		return nil, oops.Code(errcode.WriteForbidden).Errorf(`write: write_mode "delete" takes no data`)
	}
	dc := &dmlCompiler{
		conn: conn, ids: ids, paramValues: paramValues,
		collectStats: opts.Stats, collectSQL: opts.Sql, explainAnalyze: opts.QueryPlan,
	}
	if err := dc.runDeleteWhere(ctx, root); err != nil {
		return nil, err
	}
	return &WriteResult{NodeIDs: ids, Stats: dc.finalStats(), Sql: dc.finalSQL(), QueryPlan: dc.finalPlans()}, nil
}

// noData reports whether payload carries nothing : none, null, or an empty array.
func noData(payload Node) bool {
	if payload == nil || payload.Kind() == KindNull {
		return true
	}
	if payload.Kind() == KindArray {
		items, err := payload.ArrayItems()
		return err == nil && len(items) == 0
	}
	return false
}

// loadRows queues a single statement, one data-modifying CTE per table, that loads every table's rows in the same
// batch as the statements reading them.
func (dc *dmlCompiler) loadRows(ctx context.Context, rows *writeRows) error {
	tables := make([]*tempTable, 0, len(rows.batches))
	for t, b := range rows.batches {
		if b.Len() > 0 {
			tables = append(tables, t)
		}
	}
	if len(tables) == 0 {
		return nil
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].rel.PgRelId < tables[j].rel.PgRelId })
	var sql strings.Builder
	args := make([]any, 0, len(tables))
	sql.WriteString("with ")
	for i, t := range tables {
		if i > 0 {
			sql.WriteString(",\n")
		}
		q := t.general.qualified()
		fmt.Fprintf(&sql, "l%d as (insert into %s select * from unnest($%d::%s[]))", i, q, i+1, q)
		args = append(args, rows.batches[t].param())
	}
	sql.WriteString("\nselect 1")
	return dc.enqueue(ctx, nil, "loading rows", sql.String(), args)
}
