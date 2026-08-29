// Pass 4, the Writing Algorithm : denormalizes a JSON payload into the
// shared "_data" temp table (write_denormalize.go), then executes phased
// DML off it (write_dml.go) — per specs/querying.md's Writing Algorithm.
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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

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
// for each occurrence (specs/querying.md ## Writing Algorithm step 1). A
// READONLY node, and everything nested under it, is skipped entirely : its
// own children have no parent key to correlate against once it's excluded
// from "_data", so pruning has to take the whole subtree, not just the one
// node (spec : "ignored from here on out").
func assignNodeIDs(root *QueryNode) map[*QueryNode]int {
	ids := map[*QueryNode]int{}
	next := 0
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
	return ids
}

// hasDeleteComponent reports whether m's semantics include deleting rows
// absent from the payload (specs/querying.md ## Writing Algorithm step 4).
func hasDeleteComponent(m WriteMode) bool {
	switch m {
	case MERGE, MERGE_NEW, MERGE_UPDATE, DELETE_ONLY:
		return true
	default:
		return false
	}
}

// ExecuteWrite runs the whole Writing Algorithm for one request : denormalize
// payload into "_data" (already expected to exist on conn), phase 1
// (insert/update/upsert, outgoing-before-self-before-incoming, recursive),
// then phase 2 (deletes, post-order). root must already be pass-1/pass-2
// resolved (Shape/Extractors populated).
func ExecuteWrite(ctx context.Context, conn Querier, root *QueryNode, payload []byte) (*WriteResult, error) {
	ids := assignNodeIDs(root)
	if _, ok := ids[root]; !ok {
		return nil, fmt.Errorf("write: root node is readonly, nothing to write")
	}

	rows, err := denormalize(root, ids, payload)
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

	dc := &dmlCompiler{conn: conn, ids: ids}
	if err := dc.phase1(ctx, root); err != nil {
		return nil, err
	}
	if err := dc.phase2(ctx, root, nil); err != nil {
		return nil, err
	}

	return &WriteResult{NodeIDs: ids, RowCount: len(rows)}, nil
}
