// Pass 4, the Writing Algorithm : denormalizes a JSON payload into the
// shared "_data" temp table (write_denormalize.go), then executes phased
// DML off it (write_dml.go) — per specs/query-engine.md's Writing Algorithm.
// Scoped to codegen+execution only : the connection-pool/request lifecycle
// ("_data"'s creation and truncation policy, one-connection-per-request
// pinning) is deliberately out of scope here, same deferral this session
// used for pass 3's HTTP response streaming — see the approved plan. A
// caller (not built here) is expected to have already created "_data" on
// the connection it passes in.
package query

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rel-server/rel/errcode"
	"github.com/samber/oops"
)

// DataTableDDL creates "_data" if it doesn't already exist on the
// connection — the one shared source of truth for its schema, referenced by
// both this package's own tests (query/write_test.go) and the HTTP server
// (server/rel.go), so the two can't silently drift apart. "on commit
// preserve rows" is required for the real server's own request flow
// (specs/query-engine.md ## Response Shape : the write transaction commits
// before the read-back statement that builds the response runs, and that
// statement still needs to see this same request's rows) — write_test.go's
// own per-test DROP+CREATE doesn't strictly need it (nothing there commits
// mid-test), but it's harmless there either way.
const DataTableDDL = `create temp table if not exists _data (
	__row_id int primary key,
	__node_id int not null,
	__parent_id int,
	data jsonb not null,
	keys jsonb
) on commit preserve rows`

// Querier is the connection surface this package needs : enough to load
// "_data" via COPY and run the phased DML. Satisfied directly by
// *pgxpool.Conn (and *pgx.Conn), so callers/tests can pass either.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error)
}

// WriteResult is ExecuteWrite's report : just enough for a caller (or a
// test) to go find what happened in "_data" — the eventual read-back query
// (out of scope here, see package doc) is what actually reconstructs a
// response from it.
type WriteResult struct {
	// NodeIDs is every writable node's assigned __node_id (see assignNodeIDs)
	// — nodes pruned for being READONLY, or nested under a READONLY node,
	// are absent.
	NodeIDs map[*QueryNode]int

	// RowCount is the total number of "_data" rows this write produced,
	// across every writable node.
	RowCount int

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
	Path      []string
	Table     string // fully-qualified relation name, "schema.table"
	Submitted int    // rows denormalized for this node, written or not
	Inserted  int
	Updated   int
	Deleted   int
}

// SqlResult is one node's compiled statement text(s) from a `sql` request
// (specs/complex-query.md ## sql) — at most one of Insert/Update/Upsert/
// Delete is non-empty per statement kind the node actually ran ; Select is
// left for server/rel.go to fill in separately (the read-back, outside this
// package's scope).
type SqlResult struct {
	Path                           []string
	Select                         string
	Insert, Update, Upsert, Delete string
}

// PlanResult is one node's EXPLAIN (ANALYZE, FORMAT JSON) output(s) from a
// `query_plan` request on a write (specs/complex-query.md ## query_plan) —
// Select is left for server/rel.go (the read-back, outside this package's
// scope).
type PlanResult struct {
	Path                           []string
	Select                         json.RawMessage
	Insert, Update, Upsert, Delete json.RawMessage
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

// WriteState carries the __node_id/__row_id allocators across several
// ExecuteWriteState calls that share one "_data" table within a single
// request — e.g. several write items in one Sequence (specs/query-engine.md
// ## Transactions : "several queries... run in a single transaction").
// Each call must continue numbering where the previous one left off : both
// counters restart from 0 internally (assignNodeIDs/denormalize), so two
// items sharing a zero-valued WriteState would both assign __node_id 0 to
// their own root and collide on __row_id (_data's primary key) the moment
// the second item's COPY runs. The zero value is exactly what a single,
// one-off ExecuteWrite call wants — see its own doc comment.
type WriteState struct {
	nextNodeID int
	nextRowID  int
}

// ExecuteWrite runs the whole Writing Algorithm for one request : denormalize
// payload into "_data" (already expected to exist on conn), phase 1
// (insert/update/upsert, outgoing-before-self-before-incoming, recursive),
// then phase 2 (deletes, post-order). root must already be pass-1/pass-2
// resolved (Shape/Extractors populated). Equivalent to ExecuteWriteState
// with a fresh *WriteState — use that instead when "_data" is shared with
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
	return ExecuteWriteStateParamsOpts(ctx, conn, root, payload, state, paramValues, WriteOptions{Stats: collectStats})
}

// ExecuteWriteStateParamsOpts is ExecuteWriteStateParams, additionally
// shaping the response per opts (specs/complex-query.md) — an all-false
// opts is a no-op : no extra statement, row-scan, or EXPLAIN wrapping runs,
// per ## Availability's "flag off skips the work" rule.
func ExecuteWriteStateParamsOpts(ctx context.Context, conn Querier, root *QueryNode, payload []byte, state *WriteState, paramValues map[string]any, opts WriteOptions) (*WriteResult, error) {
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

	rows, nextRowID, err := denormalize(root, ids, payload, state.nextRowID)
	if err != nil {
		return nil, err
	}

	if len(rows) > 0 {
		if _, err := conn.CopyFrom(ctx,
			pgx.Identifier{"_data"},
			[]string{"__row_id", "__node_id", "__parent_id", "data"},
			pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
				r := rows[i]
				return []any{r.RowID, r.NodeID, r.ParentID, r.Data}, nil
			}),
		); err != nil {
			return nil, oops.With("row_count", len(rows)).Wrapf(err, "write: loading _data")
		}
	}

	populated := make(map[int]bool, len(ids))
	submitted := make(map[int]int, len(ids))
	for _, r := range rows {
		populated[r.NodeID] = true
		submitted[r.NodeID]++
	}

	dc := &dmlCompiler{
		conn: conn, ids: ids, populated: populated, paramValues: paramValues, submitted: submitted,
		collectStats: opts.Stats, collectSQL: opts.Sql, explainAnalyze: opts.QueryPlan,
	}
	if err := dc.phase1(ctx, root); err != nil {
		return nil, err
	}
	if err := dc.phase2(ctx, root, nil); err != nil {
		return nil, err
	}

	state.nextNodeID = nextNodeID
	state.nextRowID = nextRowID
	return &WriteResult{
		NodeIDs: ids, RowCount: len(rows),
		Stats: dc.finalStats(), Sql: dc.finalSQL(), QueryPlan: dc.finalPlans(),
	}, nil
}
