package route

// This file implements specs/new-routes.md ## Middleware : chain
// resolution lives in routeset.go (Registry.MiddlewareChain) ; this file
// runs the resolved chain against a shared transaction, merging
// request.context and side-effecting fields per the spec's own rules.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/dbauth"
	"github.com/rel-server/rel/errcode"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/static"
)

// mergeSideEffects folds resp's side-effecting fields into acc — per-key
// overwrite for headers/cookies (an earlier middleware's other keys in the
// same field still apply), whole-field overwrite for jwt/jwt_attrs/csp
// (only when resp actually set them ; nil/"" means "didn't touch this
// field", not "clear it" — jwt:null is itself a non-nil raw value, "null",
// so it still overwrites here, same as any other jwt value).
func mergeSideEffects(acc *relHttpResponsePayload, resp relHttpResponsePayload) {
	for k, v := range resp.Headers {
		if acc.Headers == nil {
			acc.Headers = map[string]json.RawMessage{}
		}
		acc.Headers[k] = v
	}
	for k, v := range resp.Cookies {
		if acc.Cookies == nil {
			acc.Cookies = map[string]json.RawMessage{}
		}
		acc.Cookies[k] = v
	}
	if resp.Jwt != nil {
		acc.Jwt = resp.Jwt
	}
	if resp.JwtAttrs != nil {
		acc.JwtAttrs = resp.JwtAttrs
	}
	if resp.Csp != "" {
		acc.Csp = resp.Csp
	}
}

// isTerminalResponse is ## Middleware's "none of status/template/
// content_type/static_file set" test — Status 0 (unset) is never a real
// HTTP status a function would deliberately send, so it doubles as
// "wasn't set" without needing a pointer/omitempty round trip.
func isTerminalResponse(resp relHttpResponsePayload) bool {
	return resp.Status != 0 || resp.Template != "" || resp.ContentType != "" || resp.StaticFile != ""
}

// mergeContext shallow-merges content's top-level keys into ctx — ## Middleware
// : "content (if any) shallow-merged into request.context ... a later
// middleware's keys win over an earlier one's on conflict."
func mergeContext(ctx map[string]json.RawMessage, content json.RawMessage) error {
	if len(content) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := sonic.Unmarshal(content, &obj); err != nil {
		return err
	}
	for k, v := range obj {
		ctx[k] = v
	}
	return nil
}

// runMiddlewareChain executes every middleware applicable to anonPath in
// order, on tx, per ## Middleware. buildReq re-derives the request JSON for
// each call (context grows as the chain proceeds) — reusing
// buildRelHttpRequest's own logic rather than patching JSON after the fact.
//
// handled is true once the request has been fully answered — either a
// terminating middleware wrote a response, or a call errored (an error
// response has already been written ; the caller's own tx.Rollback defer
// unwinds the transaction, same as any other invocation error). When
// handled is false, mergedContext (nil if no middleware ran, or none set
// content) is what the caller folds into its own final request JSON before
// proceeding, and accumulated carries every pass-through middleware's own
// cookies/headers/jwt/jwt_attrs/csp — the caller merges it into whatever
// response it ultimately renders (## Middleware : these apply "whether or
// not [a middleware] terminates the request").
func runMiddlewareChain(
	ctx context.Context,
	tx pgx.Tx,
	w http.ResponseWriter,
	r *http.Request,
	cfg *config.Config,
	reg *Registry,
	anonPath string,
	buildReq func(reqContext json.RawMessage) ([]byte, error),
	templates *TemplateSet,
	staticSrv *static.Server,
) (handled bool, mergedContext json.RawMessage, accumulated relHttpResponsePayload, err error) {
	chain := reg.MiddlewareChain(anonPath)
	if len(chain) == 0 {
		return false, nil, relHttpResponsePayload{}, nil
	}

	contextFields := map[string]json.RawMessage{}

	for _, mw := range chain {
		var reqCtx json.RawMessage
		if len(contextFields) > 0 {
			reqCtx, err = sonic.Marshal(contextFields)
			if err != nil {
				writePlainError(w, http.StatusInternalServerError, errcode.Internal, "encoding request context")
				return true, nil, accumulated, err
			}
		}
		reqJSON, err := buildReq(reqCtx)
		if err != nil {
			writePlainError(w, http.StatusInternalServerError, errcode.Internal, "encoding request")
			return true, nil, accumulated, err
		}

		pathArgValues := make([]string, len(mw.PathArgs))
		for i, name := range mw.PathArgs {
			pathArgValues[i] = chi.URLParam(r, name)
		}

		envelope, content, callErr := invokeFullControlRoute(ctx, tx, mw, reqJSON, resolvedRequestBody{}, pathArgValues)
		if callErr != nil {
			writeErrorForPgErr(w, callErr, cfg.Dev)
			return true, nil, accumulated, callErr
		}

		// ## Middleware : "nothing (NULL), letting the request proceed
		// unchanged" — a genuine SQL NULL envelope (not the JSON literal
		// "null") scans as a zero-length raw ; treat that the same as an
		// empty {} object rather than a decode error.
		var resp relHttpResponsePayload
		if len(envelope) > 0 {
			if unmarshalErr := sonic.Unmarshal(envelope, &resp); unmarshalErr != nil {
				writePlainError(w, http.StatusInternalServerError, errcode.Internal, "decoding middleware response")
				return true, nil, accumulated, unmarshalErr
			}
		}

		mergeSideEffects(&accumulated, resp)

		if isTerminalResponse(resp) {
			renderFullControlResponse(w, r, cfg, mw.Function.Identifier.String(), mw, accumulated, resp, content, templates, staticSrv)
			return true, nil, accumulated, nil
		}

		if err := mergeContext(contextFields, resp.Content); err != nil {
			writePlainError(w, http.StatusInternalServerError, errcode.Internal, "merging middleware response into request.context")
			return true, nil, accumulated, err
		}
	}

	if len(contextFields) == 0 {
		return false, nil, accumulated, nil
	}
	mergedContext, err = sonic.Marshal(contextFields)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "encoding request context")
		return true, nil, accumulated, err
	}
	return false, mergedContext, accumulated, nil
}

// GateMiddleware wraps next (== /rel, or the root-level static fallback) so
// that every applicable middleware runs first, per ## Middleware : "for a
// request resolving to a static file or /rel ... middleware runs in its
// own dedicated transaction" — distinct from whatever transaction next's
// own handler opens internally. anonPath for a concrete request is just
// r.URL.Path : neither /rel nor a static file has an anonymized form of
// its own to compare against, and middleware's own "{}" segments still
// match arbitrary literal ones (see middlewarePrefixMatches).
//
// /auth and /auth/* are exempt UNCONDITIONALLY (## Middleware's explicit
// login-recoverability exemption), checked here rather than relying solely
// on sso.Mount living outside this wrapper — an UNCONFIGURED /auth/* path
// (no matching openid/saml entry) falls through to chi's own NotFound,
// which is exactly this static/rel fallback, so the exemption has to be
// enforced here too, not just structurally by where sso.Mount is mounted.
func GateMiddleware(db *pg.DbInfos, cfg *config.Config, reg *Registry, templates *TemplateSet, staticSrv *static.Server, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth" || strings.HasPrefix(r.URL.Path, "/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		if len(reg.MiddlewareChain(r.URL.Path)) == 0 {
			next.ServeHTTP(w, r)
			return
		}

		claims, verified := jwtpkg.VerifyRequest(cfg.Jwt, r)
		if !verified && !db.AnonymousRoleExists {
			writePlainError(w, http.StatusUnauthorized, errcode.AnonymousDisabled, errcode.AnonymousDisabledMessage)
			return
		}

		ctx := r.Context()
		conn, err := db.Pool.Acquire(ctx)
		if err != nil {
			writePlainError(w, http.StatusInternalServerError, errcode.DBUnavailable, "acquiring connection")
			return
		}
		defer conn.Release()

		tx, err := conn.Begin(ctx)
		if err != nil {
			writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "starting transaction")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		if err := dbauth.SetLocalClaims(ctx, tx, claims); err != nil {
			writePlainError(w, http.StatusInternalServerError, errcode.Internal, "setting jwt claims")
			return
		}
		// No renewal here : /rel's own applyRole renews after this gate
		// passes (a second renewal here would double the Set-Cookie), and
		// the static fallback never renewed even before middleware existed.
		role := jwtpkg.ResolveRole(cfg.Pg.Query.AnonymousRole, claims, verified)
		if role == "" {
			writePlainError(w, http.StatusInternalServerError, errcode.NoRoleConfigured, dbauth.NoRoleConfiguredMessage)
			return
		}
		if err := dbauth.SetLocalRole(ctx, tx, role); err != nil {
			writeErrorForPgErr(w, err, cfg.Dev)
			return
		}

		staticInfo := staticInfoForRequest(staticSrv, r)
		buildReq := func(reqContext json.RawMessage) ([]byte, error) {
			return buildRelHttpRequest(r, json.RawMessage("null"), verified, claims, staticInfo, nil, reqContext, nil)
		}

		// mergedContext/accumulated have no consumer here — /rel and the
		// static fallback don't read request.context or an envelope's own
		// side-effecting fields the way a route function does ; a
		// pass-through middleware's cookies/headers/jwt still land on w
		// directly via applyResponseSideEffects, same as any terminating
		// middleware's would.
		handled, _, accumulated, err := runMiddlewareChain(ctx, tx, w, r, cfg, reg, r.URL.Path, buildReq, templates, staticSrv)
		if err != nil {
			return
		}
		if handled {
			_ = tx.Commit(ctx)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "committing transaction")
			return
		}
		applyResponseSideEffects(w, r, cfg, "", accumulated)
		next.ServeHTTP(w, r)
	})
}
