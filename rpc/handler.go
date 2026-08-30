package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/dbauth"
	jwtpkg "github.com/ceymard/rel/jwt"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/static"
)

// NewHandler is /rpc/{schema}/{function} : one dynamic dispatch pattern per
// ## HTTP's own wording ("dispatched dynamically... rather than registered
// as their own routes"), not one ServeMux registration per discovered
// function. staticSrv is ### Upload destinations' own write target
// (http.static.path's first directory) — nil when no static directory is
// configured/exists, in which case an upload route resolving a non-empty
// "path" is a 500 (there's nowhere to write to).
func NewHandler(db *pg.DbInfos, cfg *config.Config, reg *Registry, staticSrv *static.Server) http.Handler {
	templates := templatesForConfig(cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc/{schema}/{function}", func(w http.ResponseWriter, r *http.Request) {
		handleRpc(w, r, db, cfg, reg, templates, staticSrv)
	})
	return mux
}

func handleRpc(w http.ResponseWriter, r *http.Request, db *pg.DbInfos, cfg *config.Config, reg *Registry, templates *TemplateSet, staticSrv *static.Server) {
	ctx := r.Context()
	schema := r.PathValue("schema")
	function := r.PathValue("function")

	route, ok := reg.Lookup(schema, function, r.Method)
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Verify (Lifecycle step 2) needs no DB connection — run it first, so
	// "is this request anonymous" is knowable before anything DB-related
	// happens at all.
	claims, verified := verifyRequestJWT(cfg, r)

	// specs/jwt-roles-and-http.md "# Roles ## Anonymous role existence" and
	// "# HTTP ## Anonymous route authorization" : both checks run here,
	// BEFORE the request body is read and BEFORE a pool connection is
	// acquired — a request already known to be unauthorized never holds a
	// connection open, and never costs a connection-acquire round trip.
	if !verified {
		if !db.AnonymousRoleExists {
			writePlainError(w, http.StatusUnauthorized, "anonymous access is disabled")
			return
		}
		if !route.AnonymousAuthorized {
			writePlainError(w, http.StatusUnauthorized, "anonymous access not permitted for this route")
			return
		}
	}

	// specs/04-http-content.md ### Upload destinations : a genuinely
	// different request flow (body resolved AFTER __prepare's placement
	// decision, streamed straight to disk, never through resolveRequestBody
	// at all) — dispatched to its own handler entirely, before any of the
	// ordinary single-transaction machinery below runs.
	if route.IsUpload {
		handleUploadRoute(w, r, db, cfg, route, staticSrv, templates, verified, claims)
		return
	}

	// ## Request bodies : parses multipart/form-data or a single raw
	// binary body per route.AcceptsFiles, enforcing http.max_body_size/
	// http.max_part_count and the 415 shape-mismatch rules, and produces
	// the already-content-type-encoded RelHttpRequest.body value either
	// way (see resolveRequestBody's own doc comment).
	resolved, err := resolveRequestBody(w, r, route, int64(cfg.Http.MaxBodySize), cfg.Http.MaxPartCount)
	if err != nil {
		if rbe, ok := errors.AsType[*requestBodyError](err); ok {
			writePlainError(w, rbe.status, rbe.message)
			return
		}
		if bbe, ok := errors.AsType[*badBodyError](err); ok {
			writePlainError(w, http.StatusBadRequest, bbe.Error())
			return
		}
		writePlainError(w, http.StatusInternalServerError, "reading request body")
		return
	}

	reqJSON, err := buildRelHttpRequest(r, resolved.BodyJSON, verified, claims)
	if err != nil {
		if bqe, ok := errors.AsType[*badQueryError](err); ok {
			writePlainError(w, http.StatusBadRequest, bqe.Error())
			return
		}
		writePlainError(w, http.StatusInternalServerError, "encoding request")
		return
	}
	// Stashed for ## Templates' "Req" VarMap — the exact RelHttpRequest JSON
	// the route function itself received, not independently re-derived.
	r = r.WithContext(withRequestJSON(ctx, reqJSON))
	ctx = r.Context()

	// Only now — request known-authorized, body already fully read and
	// resolved — does a pool connection get acquired.
	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, "acquiring connection")
		return
	}
	defer conn.Release()

	// SET LOCAL ROLE only has effect for the current transaction — this
	// spans check_session, the role switch itself, and the route function
	// call, all sharing one transaction, committed only once the route
	// function has actually returned successfully.
	tx, err := conn.Begin(ctx)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, "starting transaction")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op if already committed

	if verified {
		if cfg.Http.Functions.CheckSession != "" {
			if err := dbauth.CheckSession(ctx, tx, cfg.Http.Functions.CheckSession, claims); err != nil {
				http.SetCookie(w, jwtpkg.ClearCookie(cfg.Jwt))
				writeErrorForPgErr(w, err)
				return
			}
		}
		if jwtpkg.ShouldRenew(cfg.Jwt, claims) {
			renewed := jwtpkg.Renew(cfg.Jwt, claims)
			token, serr := jwtpkg.Sign(cfg.Jwt, renewed)
			if serr == nil {
				http.SetCookie(w, jwtpkg.CookieValue(cfg.Jwt, token, renewed, ""))
			}
			claims = renewed
		}
	}

	role := cfg.Pg.Query.AnonymousRole
	if verified {
		role = jwtpkg.Role(claims)
	}
	if role == "" {
		// An empty role means query.anonymous_role was never configured — a
		// hand-built *config.Config (e.g. config.Test()) can reach this.
		// Emitting `SET LOCAL ROLE ""` would be a Postgres syntax error, and
		// skipping the role switch entirely would silently run the request
		// as whatever role the pool connection already has (a privilege
		// escalation for anonymous callers), so this is a hard error.
		writePlainError(w, http.StatusInternalServerError, "no role configured (query.anonymous_role is unset and request is anonymous)")
		return
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+dbauth.EscapeIdentifier(role)); err != nil {
		writeErrorForPgErr(w, err)
		return
	}

	raw, err := invokeRoute(ctx, tx, route, reqJSON, resolved.Files, resolved.PartsHeadersRaw)
	if err != nil {
		writeErrorForPgErr(w, err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writePlainError(w, http.StatusInternalServerError, "committing transaction")
		return
	}

	if route.MimeType != "" {
		w.Header().Set("Content-Type", route.MimeType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
		return
	}

	writeRelHttpResponse(w, r, cfg, route, raw, templates)
}

// verifyRequestJWT reads cfg.Jwt.CookieName off r and verifies it. Any
// failure (missing cookie, bad signature, expired, session-ceiling
// exceeded) is "no session", per Lifecycle step 2 — never an error in its
// own right.
func verifyRequestJWT(cfg *config.Config, r *http.Request) (jwtpkg.Claims, bool) {
	return jwtpkg.VerifyRequest(cfg.Jwt, r)
}

// invokeRoute calls route.Function with the argument list matching
// whichever of ## Request bodies' four signature shapes it was discovered
// with — (), (req), (req, files bytea[]), or (req, files bytea[],
// parts_headers jsonb) — returning the function's raw jsonb
// (RelHttpResponse) or bytea/text (mimetype domain) return value.
// partsHeadersRaw is only actually sent when route.AcceptsPartsHeaders ;
// files is always sent (as a bytea[] positional parameter — pgx encodes a
// [][]byte Go value directly, no manual array-literal building needed) for
// any route.AcceptsFiles route.
func invokeRoute(ctx context.Context, tx pgx.Tx, route Route, reqJSON []byte, files [][]byte, partsHeadersRaw json.RawMessage) ([]byte, error) {
	ident := route.Function.Identifier.EscapedString()
	var row pgx.Row
	switch {
	case route.AcceptsPartsHeaders:
		row = tx.QueryRow(ctx, "select "+ident+"($1::jsonb, $2::bytea[], $3::jsonb)", reqJSON, files, []byte(partsHeadersRaw))
	case route.AcceptsFiles:
		row = tx.QueryRow(ctx, "select "+ident+"($1::jsonb, $2::bytea[])", reqJSON, files)
	case len(route.Function.Arguments) > 0 && hasInArgument(route.Function):
		row = tx.QueryRow(ctx, "select "+ident+"($1::jsonb)", reqJSON)
	default:
		row = tx.QueryRow(ctx, "select "+ident+"()")
	}
	var raw []byte
	if err := row.Scan(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func hasInArgument(fn *pg.Function) bool {
	for i := range fn.Arguments {
		if fn.Arguments[i].IsIn() {
			return true
		}
	}
	return false
}
