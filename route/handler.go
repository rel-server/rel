package route

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/dbauth"
	"github.com/rel-server/rel/errcode"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/static"
)

// NewHandler builds a standalone chi.Router with every reg.Routes entry
// registered at its own declared path (used directly by this package's own
// tests). boot.BuildMux calls RegisterRoutes directly instead, since a
// declared route's arbitrary path has to live on the SAME router as /rel
// and the static root-fallback, not a separately-mounted sub-router.
func NewHandler(db *pg.DbInfos, cfg *config.Config, reg *Registry, staticSrv *static.Server) http.Handler {
	mux := chi.NewRouter()
	RegisterRoutes(mux, db, cfg, reg, staticSrv)
	return mux
}

// RegisterRoutes registers every reg.Routes entry at its own declared chi
// pattern on r — specs/new-routes.md replaces the old fixed
// /route/{schema}/{function} dynamic dispatch with arbitrary, per-route
// paths. staticSrv is used to compute request.static (## Static path
// masking) for every request ; nil means no http.static.path directory
// exists at all.
func RegisterRoutes(r chi.Router, db *pg.DbInfos, cfg *config.Config, reg *Registry, staticSrv *static.Server) {
	templates := templatesForConfig(cfg)
	for _, route := range reg.Routes {
		route := route
		handler := func(w http.ResponseWriter, req *http.Request) {
			handleRoute(w, req, db, cfg, reg, route, templates, staticSrv)
		}
		for _, method := range route.Methods {
			r.Method(method, route.Path, http.HandlerFunc(handler))
		}
	}
}

func handleRoute(w http.ResponseWriter, r *http.Request, db *pg.DbInfos, cfg *config.Config, reg *Registry, route Route, templates *TemplateSet, staticSrv *static.Server) {
	ctx := r.Context()

	// Verify (Lifecycle step 2) needs no DB connection — run first, so
	// anonymity is knowable before anything DB-related happens.
	claims, verified := jwtpkg.VerifyRequest(cfg.Jwt, r)

	// Both anonymous checks run before the body is read or a connection
	// acquired, so an unauthorized request never holds one.
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

	// ## `stream_upload` : a different request flow entirely (the same
	// function called twice), dispatched before the single-transaction
	// path below.
	if route.StreamUpload {
		handleUploadRoute(w, r, db, cfg, reg, route, staticSrv, templates, verified, claims)
		return
	}

	resolved, err := resolveRequestBody(w, r, route, int64(cfg.Http.MaxBodySize), cfg.Http.MaxPartCount)
	if err != nil {
		writeRequestBodyError(w, err)
		return
	}

	staticInfo := staticInfoForRequest(staticSrv, r)

	// buildReq re-derives the request JSON with a given request.context —
	// nil for the route's own call unless middleware ran and set one (##
	// Middleware).
	buildReq := func(reqContext json.RawMessage) ([]byte, error) {
		return buildRelHttpRequest(r, resolved.BodyJSON, verified, claims, staticInfo, resolved.Parts, reqContext, nil)
	}

	reqJSON, err := buildReq(nil)
	if err != nil {
		if bqe, ok := errors.AsType[*badQueryError](err); ok {
			writePlainError(w, http.StatusBadRequest, errcode.QueryMalformedJSON, bqe.Error())
			return
		}
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "encoding request")
		return
	}
	// Stashed for ## Templates' "Req" var — the exact JSON the route
	// function received, not re-derived independently. Updated again below
	// once middleware (if any) has resolved the final request.context.
	r = r.WithContext(withRequestJSON(ctx, reqJSON))
	ctx = r.Context()

	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.DBUnavailable, "acquiring connection")
		return
	}
	defer conn.Release()

	// SET LOCAL ROLE/set_config(..., true) only hold for the current
	// transaction, so middleware/role-switch/the route call all share one.
	tx, err := conn.Begin(ctx)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "starting transaction")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Exposed before middleware runs, so it (and the route function itself)
	// can read it via current_setting('rel.jwt.claims', true).
	if err := dbauth.SetLocalClaims(ctx, tx, claims); err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "setting jwt claims")
		return
	}

	// Renew (Lifecycle step 4) deliberately waits until AFTER the middleware
	// chain below decides not to reject — jwt/middleware.go's own doc
	// comment states the invariant (Renew after Check) ; a revocation
	// middleware must never hand back a freshly renewed cookie for the very
	// session it just rejected. Role resolution below reads claims' role
	// only, which renewal never changes, so using the pre-renewal claims
	// here is exact, not just adequate.
	role := jwtpkg.ResolveRole(cfg.Pg.Query.AnonymousRole, claims, verified)
	if role == "" {
		writePlainError(w, http.StatusInternalServerError, errcode.NoRoleConfigured, dbauth.NoRoleConfiguredMessage)
		return
	}
	if err := dbauth.SetLocalRole(ctx, tx, role); err != nil {
		writeErrorForPgErr(w, err, cfg.Dev)
		return
	}

	// ## Middleware : runs after the role switch, same transaction,
	// sequential — a terminating middleware or an invocation error has
	// already written the response ; either way there's nothing left to do.
	handled, mergedContext, accumulated, err := runMiddlewareChain(ctx, tx, w, r, cfg, reg, route.AnonPath, buildReq, templates, staticSrv)
	if err != nil {
		return
	}
	if handled {
		if err := tx.Commit(ctx); err != nil {
			writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "committing transaction")
			return
		}
		return
	}
	// Renew now that the chain has let the request through — a renewed
	// cookie never reaches the client for a request middleware just
	// rejected. Discarded rather than reassigned to claims : the route
	// function's own request.jwt reflects the ORIGINAL claims, same as
	// before middleware existed (buildReq's closure captures claims by
	// reference, so reassigning here would leak the renewal into a request
	// JSON built after this point).
	if verified {
		_ = jwtpkg.RenewIfDue(cfg.Jwt, w, claims)
	}

	if mergedContext != nil {
		reqJSON, err = buildReq(mergedContext)
		if err != nil {
			writePlainError(w, http.StatusInternalServerError, errcode.Internal, "encoding request")
			return
		}
		r = r.WithContext(withRequestJSON(ctx, reqJSON))
	}

	pathArgValues := make([]string, len(route.PathArgs))
	for i, name := range route.PathArgs {
		pathArgValues[i] = chi.URLParam(r, name)
	}

	if route.FullControl {
		envelope, content, err := invokeFullControlRoute(ctx, tx, route, reqJSON, resolved, pathArgValues)
		if err != nil {
			writeErrorForPgErr(w, err, cfg.Dev)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "committing transaction")
			return
		}
		writeFullControlResponse(w, r, cfg, route.Function.Identifier.String(), route, accumulated, envelope, content, templates, staticSrv)
		return
	}

	raw, err := invokeSingleReturnRoute(ctx, tx, route, reqJSON, resolved, pathArgValues)
	if err != nil {
		writeErrorForPgErr(w, err, cfg.Dev)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "committing transaction")
		return
	}
	writeSingleReturnResponse(w, r, cfg, route.Function.Identifier.String(), route, accumulated, raw, templates)
}

// buildInvokeCall builds the positional-argument SQL call for route, in
// ## Function prototype's fixed argument order : the request object (if
// HasRequestArg), then the upload-bytes argument (if any), then each
// PathArgs value in declared order.
func buildInvokeCall(route Route, reqJSON []byte, resolved resolvedRequestBody, pathArgValues []string) (string, []any) {
	ident := route.Function.Identifier.EscapedString()
	var placeholders []string
	var args []any
	pos := 1

	if route.HasRequestArg {
		placeholders = append(placeholders, fmt.Sprintf("$%d::jsonb", pos))
		args = append(args, reqJSON)
		pos++
	}
	if route.AcceptsBytes {
		placeholders = append(placeholders, fmt.Sprintf("$%d::bytea", pos))
		args = append(args, resolved.SingleBytes)
		pos++
	}
	if route.AcceptsBytesArray {
		placeholders = append(placeholders, fmt.Sprintf("$%d::bytea[]", pos))
		args = append(args, resolved.Files)
		pos++
	}
	for _, v := range pathArgValues {
		placeholders = append(placeholders, fmt.Sprintf("$%d::text", pos))
		args = append(args, v)
		pos++
	}

	call := ident + "(" + strings.Join(placeholders, ", ") + ")"
	return call, args
}

// invokeSingleReturnRoute calls a non-full-control route, returning its raw
// (already-final-form : real bytes/text/JSON, never itself JSON-wrapped)
// return value.
func invokeSingleReturnRoute(ctx context.Context, tx pgx.Tx, route Route, reqJSON []byte, resolved resolvedRequestBody, pathArgValues []string) ([]byte, error) {
	call, args := buildInvokeCall(route, reqJSON, resolved, pathArgValues)
	var raw []byte
	if err := tx.QueryRow(ctx, "select "+call, args...).Scan(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// invokeFullControlRoute calls a two-OUT-column route via "select * from
// fn(...)" — the only way to get its two OUT columns back as two separate
// result columns rather than one composite value.
func invokeFullControlRoute(ctx context.Context, tx pgx.Tx, route Route, reqJSON []byte, resolved resolvedRequestBody, pathArgValues []string) (envelope, content []byte, err error) {
	call, args := buildInvokeCall(route, reqJSON, resolved, pathArgValues)
	if err := tx.QueryRow(ctx, "select * from "+call, args...).Scan(&envelope, &content); err != nil {
		return nil, nil, err
	}
	return envelope, content, nil
}

// staticInfoForRequest computes specs/new-routes.md ## Static path masking's
// request.static — nil when there's no static.Server at all.
func staticInfoForRequest(staticSrv *static.Server, r *http.Request) *staticInfoPayload {
	if staticSrv == nil {
		return nil
	}
	reqPath := strings.TrimPrefix(r.URL.Path, "/")
	info := staticSrv.Stat(reqPath)
	payload := &staticInfoPayload{Exists: info.Exists}
	if info.Exists {
		payload.Size = info.Size
		payload.ModifiedAt = info.ModifiedAt.UTC().Format(time.RFC3339)
	}
	return payload
}
