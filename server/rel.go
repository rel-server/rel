// POST /rel : the first vertical slice wiring passes 1-4 into an actual
// HTTP request. Scope deliberately limited — see the approved plan
// (~/.claude/plans/modular-splashing-rivest.md at the time this was
// written) : ParsedQuery.Sequence (several queries sharing one transaction)
// IS handled, but ParsedQuery.WellKnown is rejected outright ; there is no
// skip-reread option ; there is no auth/role switching ; there is no
// process/config bootstrap (this package exports a http.Handler, not a
// main package).
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/query"
	"github.com/ceymard/rel/writer"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// resolvedItem is one request-body item, past parsing AND pass-1/2
// resolution — everything needed to either run its write (if any) or
// compile its response, with no further chance of a "the query itself is
// wrong" (400) error from this point on.
type resolvedItem struct {
	root    *query.QueryNode
	isWrite bool
	data    []byte
}

// NewRelHandler serves POST /rel per specs/querying.md's ## Configuration
// ("all of them MUST be POST") and ## Response Shape. db.Pool is acquired
// from once per request ; cfg drives scope/blacklist resolution exactly as
// query.ResolveContext already does in every pass-1/2 test.
func NewRelHandler(db *pg.DbInfos, cfg *config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, badRequest(fmt.Errorf("/rel only accepts POST")))
			return
		}
		handleRel(w, r, db, cfg)
	})
}

func handleRel(w http.ResponseWriter, r *http.Request, db *pg.DbInfos, cfg *config.Config) {
	ctx := r.Context()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, badRequest(fmt.Errorf("reading request body: %w", err)))
		return
	}

	pq, err := query.ParseQuery(body)
	if err != nil {
		writeError(w, badRequest(err))
		return
	}

	items := pq.Sequence
	if items == nil {
		items = []query.ParsedQuery{pq}
	}

	// Resolve every item's tree up front, before touching a connection or a
	// transaction at all — a 400 here means nothing has been opened yet,
	// nothing to roll back (see the plan's handler design, step 6).
	resolved := make([]resolvedItem, 0, len(items))
	rctx := &query.ResolveContext{Db: db, Config: cfg}
	for i, item := range items {
		if item.WellKnown != nil {
			writeError(w, badRequest(fmt.Errorf("item %d: well-known queries are not yet supported", i)))
			return
		}

		var root *query.QueryNode
		var rerr error
		isWrite := item.Write != nil
		var data []byte
		switch {
		case item.Write != nil:
			root, rerr = rctx.ResolveQuery(item.Write.Query)
			data = item.Write.Data
		case item.Relation != nil:
			root, rerr = rctx.ResolveQuery(item.Relation)
		default:
			writeError(w, badRequest(fmt.Errorf("item %d: empty query", i)))
			return
		}
		if rerr != nil {
			writeError(w, badRequest(fmt.Errorf("item %d: %w", i, rerr)))
			return
		}
		if err := rctx.ResolveExpressions(root); err != nil {
			writeError(w, badRequest(fmt.Errorf("item %d: %w", i, err)))
			return
		}
		if err := rctx.DeriveShapes(root); err != nil {
			writeError(w, badRequest(fmt.Errorf("item %d: %w", i, err)))
			return
		}
		resolved = append(resolved, resolvedItem{root: root, isWrite: isWrite, data: data})
	}

	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		writeError(w, serverError(fmt.Errorf("acquiring connection: %w", err)))
		return
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, query.DataTableDDL); err != nil {
		writeError(w, serverError(fmt.Errorf("preparing _data: %w", err)))
		return
	}
	// Truncate BEFORE doing any work too, not just after — "_data" is
	// "create ... if not exists", so on a pooled connection reused from an
	// earlier request it already exists with that request's rows still in
	// it if the release-time truncate below ever failed, was skipped by a
	// killed process, or simply hasn't run yet. Making this request's
	// correctness independent of the PREVIOUS request's cleanup having
	// succeeded is worth the (cheap, empty-table) extra statement.
	if _, err := conn.Exec(ctx, "truncate _data"); err != nil {
		writeError(w, serverError(fmt.Errorf("clearing _data: %w", err)))
		return
	}
	// Truncation also happens once, at the very end of the whole request,
	// after the response has been fully sent — specs/querying.md
	// ## Response Shape. A fresh context.Background(), not ctx : a client
	// disconnect or cancelled request must not skip this cleanup.
	defer func() { _, _ = conn.Exec(context.Background(), "truncate _data") }()

	if _, err := conn.Exec(ctx, "begin"); err != nil {
		writeError(w, serverError(fmt.Errorf("begin: %w", err)))
		return
	}

	// One shared WriteState across every write item in this request : each
	// ExecuteWriteState call must continue __node_id/__row_id numbering
	// where the previous one left off, or two write items in one Sequence
	// both start at 0 and collide on __row_id (_data's primary key) —
	// see WriteState's own doc comment.
	state := &query.WriteState{}
	nodeIDs := make([]int, len(resolved))
	for i, item := range resolved {
		if !item.isWrite {
			continue
		}
		result, err := query.ExecuteWriteState(ctx, conn, item.root, item.data, state)
		if err != nil {
			_, _ = conn.Exec(ctx, "rollback")
			writeError(w, classifyWriteError(err, i))
			return
		}
		nodeIDs[i] = result.NodeIDs[item.root]
	}

	if _, err := conn.Exec(ctx, "commit"); err != nil {
		writeError(w, serverError(fmt.Errorf("commit: %w", err)))
		return
	}

	// Compile every item's response statement BEFORE writing any response
	// bytes : once streaming starts, a failure here can no longer produce a
	// clean error envelope (the client has already received a "["), so
	// every recoverable failure must be caught first.
	statements := make([]*writer.SQLWriter, len(resolved))
	for i, item := range resolved {
		var sw *writer.SQLWriter
		var cerr error
		if item.isWrite {
			sw, cerr = query.CompileSelectForDataNode(item.root, nodeIDs[i])
		} else {
			sw, cerr = query.CompileSelect(item.root)
		}
		if cerr != nil {
			writeError(w, serverError(fmt.Errorf("item %d: compiling response: %w", i, cerr)))
			return
		}
		statements[i] = sw
	}

	w.Header().Set("Content-Type", "application/json")
	multi := len(resolved) > 1
	if multi {
		_, _ = w.Write([]byte("["))
	}
	for i, item := range resolved {
		if multi && i > 0 {
			_, _ = w.Write([]byte(","))
		}
		if err := streamItem(ctx, w, conn, item.root, statements[i]); err != nil {
			// The response is already partway through streaming (or the
			// query genuinely failed after commit) — there is no clean
			// error envelope to fall back to at this point ; the response
			// is simply truncated/invalid JSON. See response.go's
			// writeError doc comment for the same limitation.
			return
		}
	}
	if multi {
		_, _ = w.Write([]byte("]"))
	}
}

// streamItem runs one item's already-compiled statement and streams its
// result : a bare scalar for a scalar (non-setof) function root (##
// Response Shape : "the scalar of the result of a scalar function"), a
// manually-streamed JSON array otherwise.
func streamItem(ctx context.Context, w http.ResponseWriter, conn *pgxpool.Conn, root *query.QueryNode, sw *writer.SQLWriter) error {
	rows, err := conn.Query(ctx, sw.String(), sw.Args()...)
	if err != nil {
		return err
	}
	defer rows.Close()

	if root.IsFunction() && !root.Function.ReturnsSet {
		if rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			if _, err := w.Write(raw); err != nil {
				return err
			}
		}
		return rows.Err()
	}

	return streamRows(w, rows)
}

// classifyWriteError distinguishes a problem with the query/data itself
// (400) from a genuine Postgres execution error (500) : write_dml.go's own
// errors wrap a real *pgconn.PgError whenever a statement actually ran
// against Postgres and failed there (a constraint violation, say) —
// anything that DOESN'T unwrap to one is one of write_denormalize.go's own
// shape errors (a payload structure mismatch, an unsupported composite
// write), which is squarely "an error in the query/data" per this
// session's confirmed error-status rule.
func classifyWriteError(err error, item int) error {
	wrapped := fmt.Errorf("item %d: %w", item, err)
	if _, ok := errors.AsType[*pgconn.PgError](err); ok {
		return serverError(wrapped)
	}
	return badRequest(wrapped)
}
