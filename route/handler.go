package route

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/dbauth"
	"github.com/ceymard/rel/errcode"
	jwtpkg "github.com/ceymard/rel/jwt"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/static"
)

// NewHandler is /route/{schema}/{function} : one dynamic dispatch pattern per
// ## HTTP's own wording ("dispatched dynamically... rather than registered
// as their own routes"), not one ServeMux registration per discovered
// function. staticSrv is ### Upload destinations' own write target
// (http.static.path's first directory) — nil when no static directory is
// configured/exists, in which case an upload route resolving a non-empty
// "path" is a 500 (there's nowhere to write to).
func NewHandler(db *pg.DbInfos, cfg *config.Config, reg *Registry, staticSrv *static.Server) http.Handler {
	templates := templatesForConfig(cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("/route/{schema}/{function}", func(w http.ResponseWriter, r *http.Request) {
		handleRoute(w, r, db, cfg, reg, templates, staticSrv)
	})
	return mux
}

func handleRoute(w http.ResponseWriter, r *http.Request, db *pg.DbInfos, cfg *config.Config, reg *Registry, templates *TemplateSet, staticSrv *static.Server) {
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
	claims, verified := jwtpkg.VerifyRequest(cfg.Jwt, r)

	// specs/authentication.md "# Roles ## Anonymous role existence" and
	// "# HTTP ## Anonymous route authorization" : both checks run here,
	// BEFORE the request body is read and BEFORE a pool connection is
	// acquired — a request already known to be unauthorized never holds a
	// connection open, and never costs a connection-acquire round trip.
	if !verified {
		if !db.AnonymousRoleExists {
			writePlainError(w, http.StatusUnauthorized, errcode.AnonymousDisabled, errcode.AnonymousDisabledMessage)
			return
		}
		if !route.AnonymousAuthorized {
			writePlainError(w, http.StatusUnauthorized, errcode.AnonymousRouteForbidden, "anonymous access not permitted for this route")
			return
		}
	}

	// specs/http-content.md ### Upload destinations : a genuinely
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
		writeRequestBodyError(w, err)
		return
	}

	reqJSON, err := buildRelHttpRequest(r, resolved.BodyJSON, verified, claims)
	if err != nil {
		if bqe, ok := errors.AsType[*badQueryError](err); ok {
			writePlainError(w, http.StatusBadRequest, errcode.QueryMalformedJSON, bqe.Error())
			return
		}
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "encoding request")
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
		writePlainError(w, http.StatusInternalServerError, errcode.DBUnavailable, "acquiring connection")
		return
	}
	defer conn.Release()

	// SET LOCAL ROLE only has effect for the current transaction — this
	// spans check_session, the role switch itself, and the route function
	// call, all sharing one transaction, committed only once the route
	// function has actually returned successfully.
	tx, err := conn.Begin(ctx)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "starting transaction")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op if already committed

	if err := dbauth.CheckSessionIfConfigured(ctx, tx, cfg.Http.Functions.CheckSession, claims, verified); err != nil {
		jwtpkg.ClearSessionCookie(cfg.Jwt, w)
		writeErrorForPgErr(w, err, cfg.Dev)
		return
	}
	if verified {
		claims = jwtpkg.RenewIfDue(cfg.Jwt, w, claims)
	}

	role := jwtpkg.ResolveRole(cfg.Pg.Query.AnonymousRole, claims, verified)
	if role == "" {
		writePlainError(w, http.StatusInternalServerError, errcode.NoRoleConfigured, dbauth.NoRoleConfiguredMessage)
		return
	}
	if err := dbauth.SetLocalRole(ctx, tx, role); err != nil {
		writeErrorForPgErr(w, err, cfg.Dev)
		return
	}

	raw, err := invokeRoute(ctx, tx, route, reqJSON, resolved.Files, resolved.PartsHeadersRaw)
	if err != nil {
		writeErrorForPgErr(w, err, cfg.Dev)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "committing transaction")
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
