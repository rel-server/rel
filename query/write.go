// Pass 4, the Writing Algorithm : denormalizes a payload into typed temp
// tables, one per destination table (write_denormalize.go, write_temp.go), then
// executes phased DML off them (write_dml.go) — per specs/query-engine.md's
// Writing Algorithm and specs/typed-write-loading.md. The temp tables are
// created on the connection the first time a write touches their table
// (PrepareWrite, called before the request's transaction begins ; ExecuteWrite
// calls it too) and their rows go away at commit or rollback.
package query

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic/ast"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/pg"
	"github.com/samber/oops"
)

// Querier is the connection surface this package needs : enough to load
// the temp tables and run the phased DML. Satisfied directly by
// *pgxpool.Conn (and *pgx.Conn), so callers/tests can pass either. Creating the
// temp tables also needs the underlying *pgx.Conn : a wrapper must expose it
// with a Conn() *pgx.Conn method.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error)
}

// WriteResult is ExecuteWrite's report : just enough for a caller (or a
// test) to go find what happened in the temp tables — the eventual read-back query
// is what actually reconstructs a response from them.
type WriteResult struct {
	// NodeIDs is every writable node's assigned __node_id (see assignNodeIDs)
	// — nodes pruned for being READONLY, or nested under a READONLY node,
	// are absent.
	NodeIDs map[*QueryNode]int

	// RowCount is the total number of temp rows this write produced,
	// across every writable node.
	RowCount int

	// Rows is non-nil when the write also built its own response : the JSON of
	// each response row, in order (an empty, non-nil slice when there are none).
	// The caller then has no read-back to run — there are no temp rows to scope one by.
	// Nil when the response still has to be read back (query/write_fast.go).
	Rows [][]byte

	// Stats is populated only when WriteOptions.Stats was set — one entry
	// per writable node an insert/update/upsert/delete statement actually
	// ran against (specs/complex-query.md ## stats). Nil otherwise.
	Stats []Stat

	// Sql is populated only when WriteOptions.Sql was set — one entry per
	// writable node touched, the compiled text of whichever statement
	// kind(s) it ran (specs/complex-query.md ## sql). Nil otherwise.
	Sql []SqlResult

	// QueryPlan is populated only when WriteOptions.QueryPlan was set —
	// every DML statement ran as EXPLAIN (ANALYZE, FORMAT JSON) instead of
	// plainly (specs/complex-query.md ## query_plan). Nil otherwise.
	QueryPlan []PlanResult
}

// Stat is one writable node's row counts from a `stats` request
// (specs/complex-query.md ## stats).
type Stat struct {
	// Path is the chain of join-alias keys from the root down to this node
	// ; [] for the root relation itself.
	Path      []string `json:"path"`
	Table     string   `json:"table"`     // fully-qualified relation name, "schema.table"
	Submitted int      `json:"submitted"` // rows denormalized for this node, written or not
	Inserted  int      `json:"inserted"`
	Updated   int      `json:"updated"`
	Deleted   int      `json:"deleted"`
}

// SqlResult is one node's compiled statement text(s) from a `sql` request
// (specs/complex-query.md ## sql) — at most one of Insert/Update/Upsert/
// Delete is non-empty per statement kind the node actually ran ; Select is
// left for server/rel.go to fill in separately (the read-back, outside this
// package's scope).
type SqlResult struct {
	Path   []string `json:"path"`
	Select string   `json:"select,omitempty"`
	Insert string   `json:"insert,omitempty"`
	Update string   `json:"update,omitempty"`
	Upsert string   `json:"upsert,omitempty"`
	Delete string   `json:"delete,omitempty"`
}

// PlanResult is one node's EXPLAIN (ANALYZE, FORMAT JSON) output(s) from a
// `query_plan` request on a write (specs/complex-query.md ## query_plan) —
// Select is left for server/rel.go (the read-back, outside this package's
// scope).
type PlanResult struct {
	Path   []string        `json:"path"`
	Select json.RawMessage `json:"select,omitempty"`
	Insert json.RawMessage `json:"insert,omitempty"`
	Update json.RawMessage `json:"update,omitempty"`
	Upsert json.RawMessage `json:"upsert,omitempty"`
	Delete json.RawMessage `json:"delete,omitempty"`
}

// WriteOptions augments ExecuteWriteStateParamsOpts with ComplexQuery's
// write-side response-shaping flags (specs/complex-query.md). QueryPlan and
// Stats are mutually exclusive — EXPLAIN ANALYZE's plan-row output leaves no
// RowsAffected()/RETURNING data for Stats to read ; rejecting the
// combination is the caller's responsibility (server/rel.go), not enforced
// here.
type WriteOptions struct {
	Stats     bool
	QueryPlan bool
	Sql       bool
	// NoFastPath forces the general (temp table) path ; tests compare the two.
	NoFastPath bool

	// Format is how the fast path's response rows (WriteResult.Rows) are produced : JSON text, or the binary
	// records of FormatCBOR, which a ResultEncoder reads.
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
// non-writable node (## Configuration) — root.Shape.Writable alone only names the root.
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
// absent from the payload (specs/query-engine.md ## Writing Algorithm step 4).
func hasDeleteComponent(m WriteMode) bool {
	switch m {
	case MERGE, MERGE_NEW, MERGE_UPDATE, DELETE_ONLY:
		return true
	default:
		return false
	}
}

// WriteState carries the node id and row id allocators across several
// ExecuteWriteState calls that share their temp tables within a single
// request — e.g. several write items in one Sequence (specs/query-engine.md
// ## Transactions : "several queries... run in a single transaction").
// Each call must continue numbering where the previous one left off : both
// counters restart from 0 internally (assignNodeIDs/denormalize), so two
// items sharing a zero-valued WriteState would both assign node id 0 to
// their own root and share row ids, which the statements joining the temp tables by row id
// would mix up. The zero value is exactly what a single,
// one-off ExecuteWrite call wants — see its own doc comment.
type WriteState struct {
	nextNodeID int
	nextRowID  int

	// tableNodes is, per relation, the node ids that have rows loaded in its temp table so far, which
	// tells a statement whether it may leave out the "node_id = N" filter (dmlCompiler.soleNode).
	tableNodes map[*pg.Relation]map[int]bool
}

// ExecuteWrite and its ExecuteWriteState* variants always take the general path,
// leaving what they wrote in the temp tables for the caller to read back ; only
// ExecuteWriteStateParamsOpts may answer from the write itself (WriteResult.Rows).
//
// ExecuteWrite runs the whole Writing Algorithm for one request : denormalize
// payload into the temp tables (created on conn when missing, which needs conn
// to be outside a transaction : see PrepareWrite), phase 1
// (insert/update/upsert, outgoing-before-self-before-incoming, recursive),
// then phase 2 (deletes, post-order). root must already be pass-1/pass-2
// resolved (Shape/Extractors populated). Equivalent to ExecuteWriteState
// with a fresh *WriteState — use that instead when the temp tables are shared with
// other ExecuteWrite calls in the same request (see WriteState).
func ExecuteWrite(ctx context.Context, conn Querier, root *QueryNode, payload []byte) (*WriteResult, error) {
	return ExecuteWriteState(ctx, conn, root, payload, &WriteState{})
}

// ExecuteWriteState is ExecuteWrite, threading its __node_id/__row_id
// allocation through state instead of always starting both at 0 — state is
// mutated in place so the caller's next call picks up where this one left
// off.
func ExecuteWriteState(ctx context.Context, conn Querier, root *QueryNode, payload []byte, state *WriteState) (*WriteResult, error) {
	return ExecuteWriteStateParams(ctx, conn, root, payload, state, nil)
}

// ExecuteWriteStateParams is ExecuteWriteState, additionally resolving any
// $param reference (ParamExpr) the tree's own where/on_conflict/etc.
// expressions compile to against paramValues — a well-known write query's
// own per-request params (specs/well-known-queries.md ## Definition). nil
// for a plain /rel write, which never contains a ParamExpr to begin with.
func ExecuteWriteStateParams(ctx context.Context, conn Querier, root *QueryNode, payload []byte, state *WriteState, paramValues map[string]any) (*WriteResult, error) {
	return ExecuteWriteStateParamsStats(ctx, conn, root, payload, state, paramValues, false)
}

// ExecuteWriteStateParamsStats is ExecuteWriteStateParamsOpts with only
// WriteOptions.Stats settable.
func ExecuteWriteStateParamsStats(ctx context.Context, conn Querier, root *QueryNode, payload []byte, state *WriteState, paramValues map[string]any, collectStats bool) (*WriteResult, error) {
	return ExecuteWriteStateParamsOpts(ctx, conn, root, payload, state, paramValues, WriteOptions{Stats: collectStats, NoFastPath: true})
}

// ExecuteWriteStateParamsOpts is ExecuteWriteStateParams, additionally
// shaping the response per opts (specs/complex-query.md) — an all-false
// opts is a no-op : no extra statement, row-scan, or EXPLAIN wrapping runs,
// per ## Availability's "flag off skips the work" rule.
func ExecuteWriteStateParamsOpts(ctx context.Context, conn Querier, root *QueryNode, payload []byte, state *WriteState, paramValues map[string]any, opts WriteOptions) (*WriteResult, error) {
	node, err := jsonPayload(payload)
	if err != nil {
		return nil, err
	}
	return ExecuteWriteNode(ctx, conn, root, node, state, paramValues, opts)
}

// jsonPayload decodes a JSON payload ; blank text is no payload, a nil Node.
func jsonPayload(payload []byte) (Node, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return nil, nil
	}
	parsed, perr := ast.NewParser(string(trimmed)).Parse()
	if perr != 0 {
		return nil, oops.Wrapf(perr, "write: invalid payload JSON")
	}
	return sonicNode{&parsed}, nil
}

// ExecuteWriteNode is ExecuteWriteStateParamsOpts for a payload already decoded, from JSON or CBOR ; a
// nil payload is none (what a "delete" takes). The Node is only read, never retained.
func ExecuteWriteNode(ctx context.Context, conn Querier, root *QueryNode, payload Node, state *WriteState, paramValues map[string]any, opts WriteOptions) (*WriteResult, error) {
	ids, nextNodeID := assignNodeIDs(root, state.nextNodeID)
	if _, ok := ids[root]; !ok {
		return nil, oops.Code(errcode.WriteForbidden).Errorf("write: root node is readonly, nothing to write")
	}
	if bad := findUnwritableNode(root); bad != nil {
		// A function-rooted node gets its own code — specs/query-engine.md
		// ## Reading Algorithm ### Function-rooted nodes.
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
		conn: conn, ids: ids, temps: tables, tableNodes: state.tableNodes, populated: rows.populated, paramValues: paramValues, submitted: rows.submitted,
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

// PrepareWrite creates, on conn, the temp tables the writes of roots go through, and
// registers their types for it. It does nothing for a table already prepared. It must run outside
// a transaction : a rolled back transaction would take the tables away. A server calls it
// before beginning a request's transaction ; ExecuteWrite calls it itself, which is enough for a caller
// that holds no transaction.
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

// encodeContextFor builds the encoding context of conn : its session time zone, which the server
// reports as the TimeZone parameter, read the way Go loads it.
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

// loadRows queues one statement putting every table's batch of rows in, in the same batch
// as the statements that read them. The tables are loaded by data-modifying CTEs of a single statement,
// which costs one statement's overhead however many tables the request writes.
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
