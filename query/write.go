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
	"fmt"

	"github.com/ceymard/rel/errcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
}

// assignNodeIDs walks root pre-order, assigning a distinct index per node
// by tree position — never by table, so a self-join produces distinct IDs
// for each occurrence (specs/query-engine.md ## Writing Algorithm step 1). A
// READONLY node, and everything nested under it, is skipped entirely : its
// own children have no parent key to correlate against once it's excluded
// from "_data", so pruning has to take the whole subtree, not just the one
// node (spec : "ignored from here on out"). startAt lets a caller running
// several ExecuteWrite calls against the same "_data" table (several write
// items in one request, see WriteState) continue numbering where the
// previous call left off, instead of every root colliding on __node_id 0 —
// returns the next free id alongside the assignment.
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

// findUnwritableNode walks root, children first (post-order), looking for
// the ORIGINATING non-writable node — specs/query-engine.md ## Configuration :
// "A user attempting a write on such a query receives an error indicating
// the offending relation." Shape.Writable already folds every non-READONLY
// descendant's failure into each ancestor as DeriveShapes computes it
// bottom-up (shape.go), so checking root.Shape.Writable alone would catch
// the problem but could only ever name the root — visiting children first
// and returning the first (deepest) failure found is what actually
// identifies which relation's own identity columns are the problem, not
// just that somewhere under the root one is. READONLY nodes are skipped :
// writability is meaningless for a subtree with nothing to write.
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

// unwritableNodeName picks the best available display name for an error
// naming "the offending relation" : InnerName (the request's own alias) when
// set, falling back to the relation's schema-qualified identifier — a root
// node commonly has no alias of its own.
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
	ids, nextNodeID := assignNodeIDs(root, state.nextNodeID)
	if _, ok := ids[root]; !ok {
		return nil, oops.Code(errcode.WriteForbidden).Errorf("write: root node is readonly, nothing to write")
	}
	if bad := findUnwritableNode(root); bad != nil {
		// WriteForbiddenFunctionRoot specifically when the offending node is
		// function-rooted (specs/query-engine.md ## Reading Algorithm
		// ### Function-rooted nodes' own unconditionally-unwritable rule) —
		// a distinct, documented rule from the generic "identity columns
		// aren't writable" case, worth a client being able to tell apart.
		code := errcode.WriteForbidden
		if bad.IsFunction() {
			code = errcode.WriteForbiddenFunctionRoot
		}
		return nil, oops.With("relation", unwritableNodeName(bad)).Code(code).Errorf("write: relation %q is not writable — its identity columns must appear exactly once in the select output, untransformed and writable (specs/query-engine.md ## Configuration)", unwritableNodeName(bad))
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
			return nil, fmt.Errorf("write: loading _data: %w", err)
		}
	}

	populated := make(map[int]bool, len(ids))
	for _, r := range rows {
		populated[r.NodeID] = true
	}

	dc := &dmlCompiler{conn: conn, ids: ids, populated: populated, paramValues: paramValues}
	if err := dc.phase1(ctx, root); err != nil {
		return nil, err
	}
	if err := dc.phase2(ctx, root, nil); err != nil {
		return nil, err
	}

	state.nextNodeID = nextNodeID
	state.nextRowID = nextRowID
	return &WriteResult{NodeIDs: ids, RowCount: len(rows)}, nil
}
