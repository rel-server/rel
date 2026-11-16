package route

// Middleware execution : runs a chain in a shared transaction, merging request.context and side-effecting fields.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/httperr"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/reqauth"
	"github.com/rel-server/rel/static"
)

// mergeSideEffects folds resp's side-effecting fields into acc : per key for headers/cookies, whole field for
// jwt/jwt_attrs/csp when resp sets them. jwt:null is set (raw "null"), so it still overwrites.
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

// isTerminalResponse reports whether a middleware response ends the request : any of status, template,
// content_type or static_file set.
func isTerminalResponse(resp relHttpResponsePayload) bool {
	return resp.Status != 0 || resp.Template != "" || resp.ContentType != "" || resp.StaticFile != ""
}

// mergeContext shallow-merges content's top-level keys into ctx ; a later middleware's keys win.
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

// runMiddlewareChain runs every middleware applicable to anonPath in order, on tx ; buildReq derives each call's
// request JSON from the context so far.
//
// handled is true once a response was written : a terminating middleware, or an error (then err is set too).
// Otherwise mergedContext (nil when no middleware set content) goes into the route's request.context, and
// accumulated holds the pass-through middleware's side-effecting fields for the final response.
func runMiddlewareChain(
	ctx context.Context,
	tx *reqauth.Tx,
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
				httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.Internal, "encoding request context", err)
				return true, nil, accumulated, err
			}
		}
		reqJSON, err := buildReq(reqCtx)
		if err != nil {
			httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.Internal, "encoding request", err)
			return true, nil, accumulated, err
		}

		pathArgValues := make([]string, len(mw.PathArgs))
		for i, name := range mw.PathArgs {
			pathArgValues[i] = chi.URLParam(r, name)
		}

		envelope, content, callErr := invokeFullControlRoute(ctx, tx, mw, reqJSON, resolvedRequestBody{}, pathArgValues)
		if callErr != nil {
			httperr.WritePostgres(ctx, w, callErr, cfg.Dev)
			return true, nil, accumulated, callErr
		}

		// A SQL NULL envelope lets the request proceed unchanged, like {}.
		var resp relHttpResponsePayload
		if len(envelope) > 0 {
			if unmarshalErr := sonic.Unmarshal(envelope, &resp); unmarshalErr != nil {
				httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.Internal, "decoding middleware response", unmarshalErr)
				return true, nil, accumulated, unmarshalErr
			}
		}

		mergeSideEffects(&accumulated, resp)

		if isTerminalResponse(resp) {
			renderFullControlResponse(w, r, cfg, mw.Function.Identifier.String(), mw, accumulated, resp, content, templates, staticSrv)
			return true, nil, accumulated, nil
		}

		if err := mergeContext(contextFields, resp.Content); err != nil {
			httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.Internal, "merging middleware response into request.context", err)
			return true, nil, accumulated, err
		}
	}

	if len(contextFields) == 0 {
		return false, nil, accumulated, nil
	}
	mergedContext, err = sonic.Marshal(contextFields)
	if err != nil {
		httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.Internal, "encoding request context", err)
		return true, nil, accumulated, err
	}
	return false, mergedContext, accumulated, nil
}

// GateMiddleware wraps next (/rel, or the static fallback) so every applicable middleware runs first, in its own
// transaction, matched against the concrete request path.
//
// /auth and /auth/* are always exempt, so login stays reachable ; checked here too because an unconfigured /auth/*
// path falls through to this fallback.
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

		ctx := r.Context()
		sess, err := reqauth.Authenticate(cfg, db, r)
		if err != nil {
			httperr.WritePlainError(ctx, w, reqauth.HTTPError(err), cfg.Dev)
			return
		}

		// No renewal here : /rel renews itself after this gate, and the static fallback never renews.
		tx, err := sess.Open(ctx, db.Pool)
		if err != nil {
			httperr.WritePlainError(ctx, w, reqauth.HTTPError(err), cfg.Dev)
			return
		}
		defer tx.Release()
		defer func() { _ = tx.Rollback(ctx) }()

		staticInfo := staticInfoForRequest(staticSrv, r)
		buildReq := func(reqContext json.RawMessage) ([]byte, error) {
			return buildRelHttpRequest(r, json.RawMessage("null"), sess.Verified, sess.Claims, staticInfo, nil, reqContext, nil, "")
		}

		// /rel and the static fallback have no request.context ; pass-through side effects are applied to w directly.
		handled, _, accumulated, err := runMiddlewareChain(ctx, tx, w, r, cfg, reg, r.URL.Path, buildReq, templates, staticSrv)
		if err != nil {
			return
		}
		if handled {
			_ = tx.Commit(ctx)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.TransactionError, "committing transaction", err)
			return
		}
		applyResponseSideEffects(w, r, cfg, "", accumulated)
		next.ServeHTTP(w, r)
	})
}
