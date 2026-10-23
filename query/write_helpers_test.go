package query

import (
	"bytes"
	"context"

	"github.com/bytedance/sonic/ast"
	"github.com/samber/oops"
)

// Test-only conveniences over ExecuteWriteNode : JSON payloads and fewer arguments. ExecuteWrite and
// its ExecuteWriteState* variants always take the general path, leaving what they wrote in the
// temp tables for the test to read back ; only ExecuteWriteStateParamsOpts may answer from the
// write itself (WriteResult.Rows).
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
