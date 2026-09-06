package route

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/dbauth"
	"github.com/rel-server/rel/errcode"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/static"
)

// NewHandler serves /route/{schema}/{function} as one dynamic dispatch
// pattern (specs/route.md ## HTTP), not one registration per function.
// staticSrv is the upload write target (specs/http-content.md ### Upload
// destinations) ; nil means an upload route resolving a "path" is a 500.
func NewHandler(db *pg.DbInfos, cfg *config.Config, reg *Registry, staticSrv *static.Server) http.Handler {
	templates := templatesForConfig(cfg)
	mux := chi.NewRouter()
	mux.HandleFunc("/route/{schema}/{function}", func(w http.ResponseWriter, r *http.Request) {
		handleRoute(w, r, db, cfg, reg, templates, staticSrv)
	})
	return mux
}

func handleRoute(w http.ResponseWriter, r *http.Request, db *pg.DbInfos, cfg *config.Config, reg *Registry, templates *TemplateSet, staticSrv *static.Server) {
	ctx := r.Context()
	schema := chi.URLParam(r, "schema")
	function := chi.URLParam(r, "function")

	route, ok := reg.Lookup(schema, function, r.Method)
	if !ok {
		http.NotFound(w, r)
		return
	}

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

	// specs/http-content.md ### Upload destinations : a different request
	// flow entirely, dispatched before the single-transaction path below.
	if route.IsUpload {
		handleUploadRoute(w, r, db, cfg, route, staticSrv, templates, verified, claims)
		return
	}

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
	// Stashed for ## Templates' "Req" var — the exact JSON the route
	// function received, not re-derived independently.
	r = r.WithContext(withRequestJSON(ctx, reqJSON))
	ctx = r.Context()

	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.DBUnavailable, "acquiring connection")
		return
	}
	defer conn.Release()

	// SET LOCAL ROLE only holds for the current transaction, so
	// check_session/role-switch/the route call all share one.
	tx, err := conn.Begin(ctx)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "starting transaction")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

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

	WriteRelHttpResponse(w, r, cfg, route.Function.Identifier.String(), raw, templates)
}

// invokeRoute calls route.Function with the discovered ## Request bodies
// signature shape, returning its raw jsonb or bytea/text return value.
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
