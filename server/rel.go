// Package server implements POST/GET /rel — query-engine.md's Reading/
// Writing algorithms wired into an HTTP request, including a
// ParsedQuery.Sequence and a well-known query in either position query.ts's
// Query union allows (bare read, or wrapped in WriteQuery.query for a
// write), composable alongside a plain Relation in the same Sequence. See
// applyRole below, and jwt/middleware.go's own doc comment, for how the
// JWT Lifecycle splits across the two packages.
package server

import (
	"context"
	"encoding/json"
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
	"github.com/ceymard/rel/wellknown"
	"github.com/ceymard/rel/writer"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/samber/oops"
)

// resolvedItem is one request-body item, past pass-1/2 resolution — no
// further chance of a "the query itself is wrong" (400) error past this.
type resolvedItem struct {
	root    *query.QueryNode
	isWrite bool
	data    []byte

	// Non-nil only for a well-known item ; threaded into ResolveArgs so a
	// $param slot resolves without a separate code path for ordinary items.
	paramValues map[string]any

	// Non-nil only for a well-known READ item, reusing the statement
	// compiled once at load time (well-known-queries.md ## Behaviour).
	precompiledRead *writer.SQLWriter
}

// NewRelHandler serves POST/GET /rel per specs/query-engine.md's
// ## Configuration ("all of them MUST be POST") and ## Response Shape.
// db.Pool is acquired from once per request ; wkReg resolves a well-known
// item by name, rebuilt alongside db/cfg on every SIGUSR1 reload
// (boot/reload.go). Wrapped in jwt.Middleware (Lifecycle step 2/Verify
// only) — applyRole (called once handleRel has a connection) does steps
// 3 (Check), 4 (Renew), and 5 (Apply role), reading the claims
// jwt.FromContext left behind.
func NewRelHandler(db *pg.DbInfos, cfg *config.Config, wkReg *wellknown.Registry) http.Handler {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET, POST")
			writeError(w, methodNotAllowed(fmt.Errorf("/rel only accepts GET or POST")), cfg.Dev)
			return
		}
		handleRel(w, r, db, cfg, wkReg)
	})
	return jwtpkg.Middleware(cfg.Jwt)(inner)
}

// relQueryBytes returns the query.ts Query JSON the request describes :
// the POST body verbatim, or GET's own query-json.md decoding below.
func relQueryBytes(r *http.Request) ([]byte, error) {
	if r.Method == http.MethodGet {
		return decodeGETQuery(r.URL.RawQuery)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("reading request body: %w", err)
	}
	return body, nil
}

// decodeGETQuery picks the wellknown vs relation/function GET grammar by
// peeking at the "wellknown" key via the generic structural decoder first.
func decodeGETQuery(raw string) ([]byte, error) {
	tree, err := querystring.DecodeQueryField(raw)
	if err != nil {
		return nil, err
	}
	if m, ok := tree.(map[string]any); ok {
		if _, hasWellKnown := m["wellknown"]; hasWellKnown {
			return decodeWellKnownGET(m)
		}
	}
	return querystring.DecodeRelation(raw)
}

// decodeWellKnownGET builds {"wellknown", "params"?} JSON from a decoded
// GET query string ; rejects "data" (query-json.md's read-only rule).
func decodeWellKnownGET(m map[string]any) ([]byte, error) {
	name, ok := m["wellknown"].(string)
	if !ok {
		return nil, fmt.Errorf(`"wellknown" must be a plain value, not nested`)
	}
	if _, hasData := m["data"]; hasData {
		return nil, fmt.Errorf(`/rel GET is read-only ; well-known writes need POST`)
	}
	out := map[string]any{"wellknown": name}
	if p, ok := m["params"]; ok {
		out["params"] = coerceQueryStringLeaves(p)
	}
	return json.Marshal(out)
}

// coerceQueryStringLeaves re-parses each string leaf of v as JSON, keeping
// the parsed value when valid — query-json.md's %22-quoted escape hatch.
func coerceQueryStringLeaves(v any) any {
	switch t := v.(type) {
	case string:
		var parsed any
		if json.Unmarshal([]byte(t), &parsed) == nil {
			return parsed
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = coerceQueryStringLeaves(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = coerceQueryStringLeaves(vv)
		}
		return out
	default:
		return v
	}
}

// resolveWellKnownItem resolves name via wkReg (well-known-queries.md
// ## Execution Errors for the codes) ; data non-nil means a write.
func resolveWellKnownItem(wkReg *wellknown.Registry, name string, paramsRaw []byte, data []byte) (resolvedItem, error) {
	wk, ok := wkReg.Lookup(name)
	if !ok {
		return resolvedItem{}, oops.Code(errcode.WellKnownUnknownQuery).Errorf("well-known query %q is not registered", name)
	}
	paramValues, err := wk.ResolveParams(paramsRaw)
	if err != nil {
		return resolvedItem{}, err
	}
	if data != nil {
		return resolvedItem{root: wk.Root, isWrite: true, data: data, paramValues: paramValues}, nil
	}
	return resolvedItem{root: wk.Root, isWrite: false, paramValues: paramValues, precompiledRead: wk.Read}, nil
}

func handleRel(w http.ResponseWriter, r *http.Request, db *pg.DbInfos, cfg *config.Config, wkReg *wellknown.Registry) {
	ctx := r.Context()

	// authentication.md "## Roles ## Anonymous role existence" : reject
	// before reading the body or acquiring a connection when disabled.
	if _, verified := jwtpkg.FromContext(r.Context()); !verified && !db.AnonymousRoleExists {
		writeError(w, unauthorized(errcode.AnonymousDisabled, fmt.Errorf("%s", errcode.AnonymousDisabledMessage)), cfg.Dev)
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

	// query-json.md ## Scope / ## Well-known queries on GET /rel : GET
	// never decodes to a Sequence ; asserted explicitly, not just assumed.
	if r.Method == http.MethodGet && pq.Sequence != nil {
		writeError(w, badRequest(errcode.QueryMalformedJSON, fmt.Errorf("/rel GET decodes to a single relation or well-known query, not a sequence")), cfg.Dev)
		return
	}

	items := pq.Sequence
	if items == nil {
		items = []query.ParsedQuery{pq}
	}

	// Resolve every item's tree before touching a connection — a 400 here
	// means nothing has been opened yet, nothing to roll back.
	resolved := make([]resolvedItem, 0, len(items))
	rctx := &query.ResolveContext{Db: db, Config: cfg}
	for i, item := range items {
		// Bare (item.WellKnown) or wrapped (item.Write.WellKnown) — both
		// already resolved at load time, just a name lookup here.
		if wk := item.WellKnown; wk != nil {
			ri, wkErr := resolveWellKnownItem(wkReg, wk.WellKnown, wk.Params, nil)
			if wkErr != nil {
				writeError(w, badRequest(codeOrUnclassified(wkErr), fmt.Errorf("item %d: %w", i, wkErr)), cfg.Dev)
				return
			}
			resolved = append(resolved, ri)
			continue
		}
		if item.Write != nil && item.Write.WellKnown != nil {
			wk := item.Write.WellKnown
			ri, wkErr := resolveWellKnownItem(wkReg, wk.WellKnown, wk.Params, item.Write.Data)
			if wkErr != nil {
				writeError(w, badRequest(codeOrUnclassified(wkErr), fmt.Errorf("item %d: %w", i, wkErr)), cfg.Dev)
				return
			}
			resolved = append(resolved, ri)
			continue
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
			writeError(w, badRequest(codeOrUnclassified(rerr), fmt.Errorf("item %d: %w", i, rerr)), cfg.Dev)
			return
		}
		if err := rctx.ResolveExpressions(root); err != nil {
			writeError(w, badRequest(codeOrUnclassified(err), fmt.Errorf("item %d: %w", i, err)), cfg.Dev)
			return
		}
		if err := rctx.DeriveShapes(root); err != nil {
			writeError(w, badRequest(codeOrUnclassified(err), fmt.Errorf("item %d: %w", i, err)), cfg.Dev)
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
	// "_data" is per-connection ; granted to PUBLIC every request since a
	// switched role (applyRole below) has no default privileges on it.
	if _, err := conn.Exec(ctx, "grant all on _data to public"); err != nil {
		writeError(w, serverError(errcode.TransactionError, fmt.Errorf("granting _data: %w", err)), cfg.Dev)
		return
	}
	// Truncated before use too : a pooled connection may still hold a
	// previous request's rows if the release-time truncate below never ran.
	if _, err := conn.Exec(ctx, "truncate _data"); err != nil {
		writeError(w, serverError(errcode.TransactionError, fmt.Errorf("clearing _data: %w", err)), cfg.Dev)
		return
	}
	// Also truncated after the response is fully sent — context.Background(),
	// not ctx, so a client disconnect can't skip this cleanup.
	defer func() { _, _ = conn.Exec(context.Background(), "truncate _data") }()

	if _, err := conn.Exec(ctx, "begin"); err != nil {
		writeError(w, serverError(errcode.TransactionError, fmt.Errorf("begin: %w", err)), cfg.Dev)
		return
	}

	// Lifecycle steps 3-5 run here, needing this connection ; SET LOCAL
	// ROLE reverts at the single commit/rollback below (## Transactions).
	if err := applyRole(ctx, w, r, conn, cfg); err != nil {
		_, _ = conn.Exec(ctx, "rollback")
		writeError(w, err, cfg.Dev)
		return
	}

	// One shared WriteState across every write item : __row_id numbering
	// must continue across items, or two writes in one Sequence collide.
	state := &query.WriteState{}
	nodeIDs := make([]int, len(resolved))
	for i, item := range resolved {
		if !item.isWrite {
			continue
		}
		result, err := query.ExecuteWriteStateParams(ctx, conn, item.root, item.data, state, item.paramValues)
		if err != nil {
			_, _ = conn.Exec(ctx, "rollback")
			writeError(w, classifyWriteError(err, i), cfg.Dev)
			return
		}
		nodeIDs[i] = result.NodeIDs[item.root]
	}

	// NO commit here : ## Transactions keeps the write phase and every
	// item's read-back in one transaction, committed only at the very end.

	// Compile every statement before writing any response bytes : once
	// streaming starts there's no clean error envelope left to fall back to.
	statements := make([]*writer.SQLWriter, len(resolved))
	args := make([][]any, len(resolved))
	for i, item := range resolved {
		var sw *writer.SQLWriter
		var cerr error
		switch {
		case item.isWrite:
			sw, cerr = query.CompileSelectForDataNode(item.root, nodeIDs[i])
		case item.precompiledRead != nil:
			sw = item.precompiledRead
		default:
			sw, cerr = query.CompileSelect(item.root)
		}
		if cerr != nil {
			writeError(w, serverError(errcode.Internal, fmt.Errorf("item %d: compiling response: %w", i, cerr)), cfg.Dev)
			return
		}
		statements[i] = sw
		// ResolveArgs, not Args() : degrades to Args() when paramValues is
		// nil, but also resolves a well-known item's named $param slots.
		a, aerr := sw.ResolveArgs(item.paramValues)
		if aerr != nil {
			writeError(w, serverError(errcode.Internal, fmt.Errorf("item %d: resolving params: %w", i, aerr)), cfg.Dev)
			return
		}
		args[i] = a
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
		// A clean envelope is only possible for a single-item request's
		// very first byte — see response.go's writeError doc comment.
		cleanErrorPossible := !multi && i == 0
		if err := streamItem(ctx, w, conn, item.root, statements[i], args[i], cleanErrorPossible); err != nil {
			// A read failing rolls back the whole transaction too (##
			// Transactions) ; explicit, though pgxpool would discard it anyway.
			_, _ = conn.Exec(ctx, "rollback")
			if cse, ok := errors.AsType[*cleanStreamError](err); ok {
				writeError(w, classifyReadError(cse.Unwrap(), i), cfg.Dev)
			}
			// Otherwise streaming already started — truncated JSON is the
			// only signal left (writeError's own doc comment covers this).
			return
		}
	}

	// The single commit for the whole request — ## Transactions accepts
	// the risk of a completed-looking response over buffering it whole.
	if _, err := conn.Exec(ctx, "commit"); err != nil {
		logging.FromContext(ctx).With("module", "server").Error("commit failed after streaming had already started", "error", err.Error())
		return
	}
	if multi {
		_, _ = w.Write([]byte("]"))
	}
}

// cleanStreamError marks a streamItem failure before any response bytes
// were written — the caller can still fall back to writeError's envelope.
type cleanStreamError struct{ err error }

func (e *cleanStreamError) Error() string { return e.err.Error() }
func (e *cleanStreamError) Unwrap() error { return e.err }

// streamItem runs sw and streams the result : a bare scalar for a scalar
// function root, a JSON array otherwise (## Response Shape).
func streamItem(ctx context.Context, w http.ResponseWriter, conn *pgxpool.Conn, root *query.QueryNode, sw *writer.SQLWriter, args []any, cleanErrorPossible bool) error {
	rows, err := conn.Query(ctx, sw.String(), args...)
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

// applyRole runs Lifecycle steps 3-5 (Check/Renew/Apply role) on conn,
// inside an open transaction — SET LOCAL ROLE reverts at commit/rollback.
func applyRole(ctx context.Context, w http.ResponseWriter, r *http.Request, conn *pgxpool.Conn, cfg *config.Config) error {
	claims, verified := jwtpkg.FromContext(r.Context())

	// ClearSessionCookie's own Header().Del guards a stray Set-Cookie ;
	// currently a no-op since Renew runs after this, kept for safety.
	if cerr := dbauth.CheckSessionIfConfigured(ctx, conn, cfg.Http.Functions.CheckSession, claims, verified); cerr != nil {
		jwtpkg.ClearSessionCookie(cfg.Jwt, w)
		return classifyCheckSessionError(cerr)
	}
	if verified {
		claims = jwtpkg.RenewIfDue(cfg.Jwt, w, claims)
	}

	role := jwtpkg.ResolveRole(cfg.Pg.Query.AnonymousRole, claims, verified)
	if role == "" {
		return serverError(errcode.NoRoleConfigured, fmt.Errorf("%s", dbauth.NoRoleConfiguredMessage))
	}
	if serr := dbauth.SetLocalRole(ctx, conn, role); serr != nil {
		return serverError(errcode.Internal, fmt.Errorf("applying role: %w", serr))
	}
	return nil
}

// codeOrUnclassified reads back the query package's own oc.Code(...) tag
// off err (or a wrapped ancestor), falling back to errcode.Unclassified.
func codeOrUnclassified(err error) errcode.Code {
	oe, ok := oops.AsOops(err)
	if !ok {
		return errcode.Unclassified
	}
	if code, ok := oe.Code().(errcode.Code); ok {
		return code
	}
	return errcode.Unclassified
}

// classifyOrFallback routes wrapped through pgerr.Classify (PG_*/RSxxx),
// falling back to fallback(wrapped) when it isn't a *pgconn.PgError.
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

// classifyWriteError classifies via pgerr.Classify, never wrapped.Error()
// (which can embed generated SQL text) ; anything else is a plain 400.
func classifyWriteError(err error, item int) *requestError {
	return classifyOrFallback(fmt.Errorf("item %d: %w", item, err), func(e error) *requestError {
		return badRequest(codeOrUnclassified(err), e)
	})
}

// classifyReadError mirrors classifyWriteError, but a read failure was
// never "bad data" — unclassified falls back to 500, not 400.
func classifyReadError(err error, item int) *requestError {
	return classifyOrFallback(fmt.Errorf("item %d: %w", item, err), func(e error) *requestError {
		return serverError(errcode.Internal, e)
	})
}
