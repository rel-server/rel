// POST /rel : the first vertical slice wiring passes 1-4 into an actual
// HTTP request. Scope deliberately limited — see the approved plan
// (~/.claude/plans/modular-splashing-rivest.md at the time this was
// written) : ParsedQuery.Sequence (several queries sharing one transaction)
// IS handled, but ParsedQuery.WellKnown is rejected outright ; there is no
// skip-reread option ; there is no process/config bootstrap (this package
// exports a http.Handler, not a main package). Auth/role switching now
// exists — see applyRole below and jwt/middleware.go's own doc comment for
// how the JWT Lifecycle splits across the two packages.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/dbauth"
	"github.com/ceymard/rel/errcode"
	jwtpkg "github.com/ceymard/rel/jwt"
	"github.com/ceymard/rel/logging"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/pgerr"
	"github.com/ceymard/rel/query"
	"github.com/ceymard/rel/querystring"
	"github.com/ceymard/rel/writer"
	"github.com/jackc/pgx/v5/pgxpool"
)

// log is this package's own module-tagged logger — specs/logging.md
// ## Domain scoping's convention, one per package.
var log = logging.For("server")

// resolvedItem is one request-body item, past parsing AND pass-1/2
// resolution — everything needed to either run its write (if any) or
// compile its response, with no further chance of a "the query itself is
// wrong" (400) error from this point on.
type resolvedItem struct {
	root    *query.QueryNode
	isWrite bool
	data    []byte
}

// NewRelHandler serves POST /rel per specs/query-engine.md's ## Configuration
// ("all of them MUST be POST") and ## Response Shape. db.Pool is acquired
// from once per request ; cfg drives scope/blacklist resolution exactly as
// query.ResolveContext already does in every pass-1/2 test. Wrapped in
// jwt.Middleware (Lifecycle steps 2/Verify and 4/Renew) — handleRel itself
// does step 3/Check and step 5/Apply role once it has a connection, reading
// the (possibly-renewed) claims jwt.FromContext left behind.
func NewRelHandler(db *pg.DbInfos, cfg *config.Config) http.Handler {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET, POST")
			writeError(w, methodNotAllowed(fmt.Errorf("/rel only accepts GET or POST")), cfg.Dev)
			return
		}
		handleRel(w, r, db, cfg)
	})
	return jwtpkg.Middleware(cfg.Jwt)(inner)
}

// relQueryBytes returns the query.ts Query JSON this request describes :
// the POST body verbatim, or — for GET, per specs/query-json.md — the
// query string decoded through querystring.DecodeRelation, which already
// enforces the read-only/single-relation restriction (## Scope) before
// this function ever sees the result.
func relQueryBytes(r *http.Request) ([]byte, error) {
	if r.Method == http.MethodGet {
		return querystring.DecodeRelation(r.URL.RawQuery)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("reading request body: %w", err)
	}
	return body, nil
}

func handleRel(w http.ResponseWriter, r *http.Request, db *pg.DbInfos, cfg *config.Config) {
	ctx := r.Context()

	// specs/authentication.md "# Roles ## Anonymous role existence" :
	// with anonymous access disabled, every unauthenticated request is
	// rejected with 401 immediately — before the body is read, before a
	// pool connection is acquired. jwtpkg.Middleware already ran
	// Verify/Renew before this handler runs (see NewRelHandler's own doc
	// comment), so claims/verified are already known with no DB access
	// needed here.
	if _, verified := jwtpkg.FromContext(r.Context()); !verified && !db.AnonymousRoleExists {
		writeError(w, unauthorized(errcode.AnonymousDisabled, fmt.Errorf("anonymous access is disabled")), cfg.Dev)
		return
	}

	body, err := relQueryBytes(r)
	if err != nil {
		writeError(w, badRequest(errcode.MalformedBody, err), cfg.Dev)
		return
	}

	pq, err := query.ParseQuery(body)
	if err != nil {
		writeError(w, badRequest(errcode.QueryMalformedJSON, err), cfg.Dev)
		return
	}

	// GET /rel decodes to exactly one Relation (specs/query-json.md ##
	// Scope) — querystring.DecodeRelation only ever produces a bare
	// Relation object, never a sequence, but this is still worth asserting
	// explicitly : a silent Sequence branch here would defeat the whole
	// point of the read-only/single-relation restriction if the decoder
	// ever grew a way to produce one.
	if r.Method == http.MethodGet && pq.Sequence != nil {
		writeError(w, badRequest(errcode.QueryMalformedJSON, fmt.Errorf("/rel GET decodes to a single relation, not a sequence")), cfg.Dev)
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
			writeError(w, badRequest(errcode.WellKnownQueryUnsupported, fmt.Errorf("item %d: well-known queries are not yet supported", i)), cfg.Dev)
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
			writeError(w, badRequest(errcode.QueryMalformedJSON, fmt.Errorf("item %d: empty query", i)), cfg.Dev)
			return
		}
		if rerr != nil {
			// A catch-all bucket, deliberately : ResolveQuery's own failures
			// span several of specs/error-handling.md's more specific
			// QUERY_* codes (unknown identifier, join eligibility, write
			// forbidden, ...) that the query package doesn't yet tag at the
			// point they're raised — see specs/TODO.md's note on this gap.
			// errcode.Unclassified over a fabricated specific code : false
			// precision would be worse than an honest "not yet classified."
			writeError(w, badRequest(errcode.Unclassified, fmt.Errorf("item %d: %w", i, rerr)), cfg.Dev)
			return
		}
		if err := rctx.ResolveExpressions(root); err != nil {
			writeError(w, badRequest(errcode.Unclassified, fmt.Errorf("item %d: %w", i, err)), cfg.Dev)
			return
		}
		if err := rctx.DeriveShapes(root); err != nil {
			writeError(w, badRequest(errcode.Unclassified, fmt.Errorf("item %d: %w", i, err)), cfg.Dev)
			return
		}
		resolved = append(resolved, resolvedItem{root: root, isWrite: isWrite, data: data})
	}

	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		writeError(w, serverError(errcode.DBUnavailable, fmt.Errorf("acquiring connection: %w", err)), cfg.Dev)
		return
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, query.DataTableDDL); err != nil {
		writeError(w, serverError(errcode.TransactionError, fmt.Errorf("preparing _data: %w", err)), cfg.Dev)
		return
	}
	// "_data" is owned by the connecting role (whoever ran the CREATE TEMP
	// TABLE above), so a request running under a switched role (see
	// applyRole below) has no privileges on it by default — granted to
	// PUBLIC unconditionally, every request, since it's a per-connection
	// temp table : a fresh physical connection means a fresh, ungranted
	// "_data" even though the CREATE itself is a same-request no-op after
	// the first. Not a privilege concern in its own right : "_data" is
	// request-scoped scratch space, invisible to any other session.
	if _, err := conn.Exec(ctx, "grant all on _data to public"); err != nil {
		writeError(w, serverError(errcode.TransactionError, fmt.Errorf("granting _data: %w", err)), cfg.Dev)
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
		writeError(w, serverError(errcode.TransactionError, fmt.Errorf("clearing _data: %w", err)), cfg.Dev)
		return
	}
	// Truncation also happens once, at the very end of the whole request,
	// after the response has been fully sent — specs/query-engine.md
	// ## Response Shape. A fresh context.Background(), not ctx : a client
	// disconnect or cancelled request must not skip this cleanup.
	defer func() { _, _ = conn.Exec(context.Background(), "truncate _data") }()

	if _, err := conn.Exec(ctx, "begin"); err != nil {
		writeError(w, serverError(errcode.TransactionError, fmt.Errorf("begin: %w", err)), cfg.Dev)
		return
	}

	// JWT Lifecycle steps 3 (Check) and 5 (Apply role) — see
	// jwt.Middleware's own doc comment for why these two, unlike Verify/
	// Renew, run here rather than as generic middleware : both need this
	// connection, which doesn't exist yet when the middleware runs.
	// SET LOCAL ROLE, not session-scoped SET ROLE : the whole request now
	// runs as ONE transaction, write phase and every item's read-back alike
	// (specs/query-engine.md ## Transactions) — a transaction-scoped role
	// applies for the entire thing and reverts automatically at the single
	// commit/rollback below, with no separate RESET ROLE cleanup needed.
	if err := applyRole(ctx, w, r, conn, cfg); err != nil {
		_, _ = conn.Exec(ctx, "rollback")
		writeError(w, err, cfg.Dev)
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
			writeError(w, classifyWriteError(err, i), cfg.Dev)
			return
		}
		nodeIDs[i] = result.NodeIDs[item.root]
	}

	// NO commit here : specs/query-engine.md ## Transactions now keeps the
	// write phase and every item's read-back in the SAME transaction, all
	// the way through streaming below — the actual commit is the very last
	// thing this handler does, after every item (write AND read alike) has
	// fully succeeded.

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
			writeError(w, serverError(errcode.Internal, fmt.Errorf("item %d: compiling response: %w", i, cerr)), cfg.Dev)
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
		// A clean error envelope is only still possible for the very first
		// byte of a single-item response — nothing (not even "[") has been
		// written yet at that point. Role switching makes a read-time
		// permission-denied error (the query itself fails at conn.Query,
		// before any row is scanned) a routine outcome, not a pathological
		// one, so this is worth the one extra branch — everywhere else, the
		// response is already partway through streaming and there's no
		// clean envelope to fall back to (see response.go's writeError doc
		// comment for that inherent limitation).
		cleanErrorPossible := !multi && i == 0
		if err := streamItem(ctx, w, conn, item.root, statements[i], cleanErrorPossible); err != nil {
			// A read failing now rolls back the whole transaction, write
			// phase included — specs/query-engine.md ## Transactions : a
			// read calling into a function that "goes awry" must not leave
			// an already-committed write behind it. pgxpool would notice
			// the connection isn't idle and discard it on Release() even
			// without this, but an explicit rollback states the intent
			// plainly rather than relying on that as the only signal.
			_, _ = conn.Exec(ctx, "rollback")
			if cse, ok := errors.AsType[*cleanStreamError](err); ok {
				writeError(w, classifyReadError(cse.Unwrap(), i), cfg.Dev)
			}
			// Otherwise the response is already partway through streaming —
			// there is no clean error envelope to fall back to at this
			// point ; the response is simply truncated/invalid JSON. See
			// response.go's writeError doc comment for the same limitation.
			return
		}
	}

	// The single commit for the whole request, only now that every item —
	// write AND read-back alike — has fully succeeded. A failure here is
	// a genuinely new, accepted risk this ordering creates (specs/
	// query-engine.md ## Transactions spells it out in full) : for a
	// multi-item request, skipping the closing "]" below at least leaves
	// the client with truncated, unparseable JSON — a real, if blunt,
	// failure signal. For a SINGLE-item request there is no such cue : its
	// entire response may already be fully streamed and look completely
	// valid by the time this fails, so a client can in principle observe
	// what looks like a successful response for a write that was then
	// rolled back. There is no way to avoid this without buffering the
	// whole response first, which ## Response Shape already rejects for
	// memory reasons.
	if _, err := conn.Exec(ctx, "commit"); err != nil {
		log.Error("commit failed after streaming had already started", "error", err.Error())
		return
	}
	if multi {
		_, _ = w.Write([]byte("]"))
	}
}

// cleanStreamError marks a streamItem failure that happened before any
// response bytes were written — the caller can still fall back to
// writeError's clean JSON envelope instead of the usual silent truncation.
type cleanStreamError struct{ err error }

func (e *cleanStreamError) Error() string { return e.err.Error() }
func (e *cleanStreamError) Unwrap() error { return e.err }

// streamItem runs one item's already-compiled statement and streams its
// result : a bare scalar for a scalar (non-setof) function root (##
// Response Shape : "the scalar of the result of a scalar function"), a
// manually-streamed JSON array otherwise. cleanErrorPossible is true only
// for a single-item request's first (only) item, before anything has been
// written yet.
//
// pgx's Query itself rarely errors : execution failures (a permission
// error, say) are deferred to the first rows.Next()/rows.Err() call
// instead, per pgx's own lazy-Query design — so "before anything is
// written" means peeking one row BEFORE writing the opening "[", not just
// checking Query's own return error. Any failure surfacing after that peek
// (a later row, a write) can't safely produce a clean envelope any more —
// the response may already be partway through streaming.
func streamItem(ctx context.Context, w http.ResponseWriter, conn *pgxpool.Conn, root *query.QueryNode, sw *writer.SQLWriter, cleanErrorPossible bool) error {
	rows, err := conn.Query(ctx, sw.String(), sw.Args()...)
	if err != nil {
		if cleanErrorPossible {
			return &cleanStreamError{err}
		}
		return err
	}
	defer rows.Close()

	if root.IsFunction() && !root.Function.ReturnsSet {
		hasRow := rows.Next()
		if err := rows.Err(); err != nil {
			if cleanErrorPossible {
				return &cleanStreamError{err}
			}
			return err
		}
		if hasRow {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			if _, err := w.Write(raw); err != nil {
				return err
			}
		}
		return nil
	}

	return streamRows(w, rows, cleanErrorPossible)
}

// applyRole runs Lifecycle steps 3 (Check) and 5 (Apply role) on conn,
// using the claims jwt.Middleware already verified/renewed. conn must
// already be inside an open transaction — SET LOCAL ROLE is transaction-
// scoped and reverts automatically at that transaction's own commit/
// rollback, so unlike an earlier version of this function there is no
// cleanup closure to run : the caller's own rollback-on-error path (and,
// on success, the request's single final commit) already does it. Any
// returned error is already a *requestError, ready for writeError.
func applyRole(ctx context.Context, w http.ResponseWriter, r *http.Request, conn *pgxpool.Conn, cfg *config.Config) error {
	claims, verified := jwtpkg.FromContext(r.Context())

	if verified && cfg.Http.Functions.CheckSession != "" {
		if cerr := dbauth.CheckSession(ctx, conn, cfg.Http.Functions.CheckSession, claims); cerr != nil {
			// jwt.Middleware runs Renew (step 4) BEFORE this handler ever
			// gets to run Check (step 3) — the reverse of the spec's own
			// step order, unavoidable here since Renew needs no DB and
			// Check does (see jwt/middleware.go's doc comment). A renewed-
			// but-now-rejected session would otherwise leave TWO Set-Cookie
			// headers on the response (http.SetCookie uses Header().Add,
			// not Set) : a still-valid renewed token, then the clear.
			// Nothing else can have set a cookie on /rel before this point,
			// so clearing the header outright is safe.
			w.Header().Del("Set-Cookie")
			http.SetCookie(w, jwtpkg.ClearCookie(cfg.Jwt))
			return classifyCheckSessionError(cerr)
		}
	}

	role := cfg.Pg.Query.AnonymousRole
	if verified {
		role = jwtpkg.Role(claims)
	}
	if role == "" {
		// Same reasoning as rpc/handler.go's identical guard : an empty
		// role means query.anonymous_role was never configured (reachable
		// via a hand-built *config.Config, e.g. config.Test()). Emitting
		// `SET LOCAL ROLE ""` is a Postgres syntax error, and skipping the
		// switch entirely would silently run the request as whatever role
		// the pool connection already has — a privilege escalation for
		// anonymous callers — so this is a hard error either way.
		return serverError(errcode.NoRoleConfigured, fmt.Errorf("no role configured (query.anonymous_role is unset and request is anonymous)"))
	}
	if _, serr := conn.Exec(ctx, "SET LOCAL ROLE "+dbauth.EscapeIdentifier(role)); serr != nil {
		return serverError(errcode.Internal, fmt.Errorf("applying role: %w", serr))
	}
	return nil
}

// classifyCheckSessionError maps an exception raised by
// http.functions.check_session — RSxxx first (## Postgres Exceptions), then
// pgerr's PG_* table, same pgerr.Classify every Postgres-execution error in
// this file now goes through. The JSON envelope (not plain text) still
// applies here : /rel and /rpc keep their own separate error framings per
// their respective spec sections, this is only the classification shared.
// classifyOrFallback routes wrapped through pgerr.Classify, returning the
// classified *requestError (built from the classified Detail, never from
// wrapped.Error() — see classifyWriteError's own doc comment for why that
// matters : a wrapped chain can embed generated SQL text) when it unwraps
// to a *pgconn.PgError, or fallback(wrapped) otherwise. The shared shape
// behind classifyCheckSessionError/classifyWriteError/classifyReadError,
// which differ only in their own wrap-context and which status/code
// applies when nothing classifies.
func classifyOrFallback(wrapped error, fallback func(error) *requestError) *requestError {
	if status, code, tier, detail, ok := pgerr.Classify(wrapped); ok {
		return pgClassified(status, code, tier, detail, wrapped)
	}
	return fallback(wrapped)
}

func classifyCheckSessionError(err error) *requestError {
	return classifyOrFallback(fmt.Errorf("check_session: %w", err), func(e error) *requestError {
		return serverError(errcode.Internal, e)
	})
}

// classifyWriteError distinguishes a problem with the query/data itself
// (400) from a genuine Postgres execution error : write_dml.go's own errors
// wrap a real *pgconn.PgError whenever a statement actually ran against
// Postgres and failed there (a constraint violation, say) — routed through
// pgerr.Classify so the response is built from the classified Detail, never
// from wrapped.Error() (which embeds write_dml.go's own "insert:
// %w\nsql: %s"-style generated-SQL text — see specs/error-handling.md's
// codegen-fingerprinting concern, a live leak this classification closes).
// Anything that DOESN'T unwrap to a *pgconn.PgError is one of
// write_denormalize.go's own shape errors (a payload structure mismatch, an
// unsupported composite write) — squarely "an error in the query/data."
func classifyWriteError(err error, item int) *requestError {
	return classifyOrFallback(fmt.Errorf("item %d: %w", item, err), func(e error) *requestError {
		return badRequest(errcode.Unclassified, e)
	})
}

// classifyReadError is classifyWriteError's counterpart for a read that
// failed cleanly (streamItem's *cleanStreamError case, single-item
// requests only — see the caller's own comment) : the switched-role SELECT
// can just as easily surface a permission-denied error, or an RSxxx raised
// by a function it calls, as any write can. Unlike a write failure, a read
// failure was never "bad data" in the request — anything unclassified here
// falls back to a genuine 500, not badRequest.
func classifyReadError(err error, item int) *requestError {
	return classifyOrFallback(fmt.Errorf("item %d: %w", item, err), func(e error) *requestError {
		return serverError(errcode.Internal, e)
	})
}
