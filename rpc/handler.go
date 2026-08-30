package rpc

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/dbauth"
	jwtpkg "github.com/ceymard/rel/jwt"
	"github.com/ceymard/rel/pg"
)

// NewHandler is /rpc/{schema}/{function} : one dynamic dispatch pattern per
// ## HTTP's own wording ("dispatched dynamically... rather than registered
// as their own routes"), not one ServeMux registration per discovered
// function.
func NewHandler(db *pg.DbInfos, cfg *config.Config, reg *Registry) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc/{schema}/{function}", func(w http.ResponseWriter, r *http.Request) {
		handleRpc(w, r, db, cfg, reg)
	})
	return mux
}

func handleRpc(w http.ResponseWriter, r *http.Request, db *pg.DbInfos, cfg *config.Config, reg *Registry) {
	ctx := r.Context()
	schema := r.PathValue("schema")
	function := r.PathValue("function")

	route, ok := reg.Lookup(schema, function, r.Method)
	if !ok {
		http.NotFound(w, r)
		return
	}

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

	claims, verified := verifyRequestJWT(cfg, r)
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

	role := cfg.Pg.Anonymous
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

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writePlainError(w, http.StatusBadRequest, "reading request body")
		return
	}

	reqJSON, err := buildRelHttpRequest(r, body, verified, claims)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, "encoding request")
		return
	}

	raw, err := invokeRoute(ctx, tx, route, reqJSON)
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

	writeRelHttpResponse(w, cfg, route, raw)
}

// verifyRequestJWT reads cfg.Jwt.CookieName off r and verifies it. Any
// failure (missing cookie, bad signature, expired, session-ceiling
// exceeded) is "no session", per Lifecycle step 2 — never an error in its
// own right.
func verifyRequestJWT(cfg *config.Config, r *http.Request) (jwtpkg.Claims, bool) {
	cookie, err := r.Cookie(cfg.Jwt.CookieName)
	if err != nil {
		return nil, false
	}
	claims, err := jwtpkg.Verify(cfg.Jwt, cookie.Value)
	if err != nil {
		// Failure is never an error for the request itself (Lifecycle step
		// 2 treats it as "no session"), but a forged/tampered token and an
		// honestly-expired one are operationally very different, and this
		// is the only signal an operator gets for either — never log the
		// token itself.
		slog.Default().Debug("rpc: jwt verification failed, treating as anonymous", "path", r.URL.Path, "error", err.Error())
		return nil, false
	}
	return claims, true
}

// invokeRoute calls route.Function with reqJSON as its single argument (or
// no arguments at all for a 0-arg route), returning the function's raw
// jsonb (RelHttpResponse) or bytea (mimetype domain) return value.
func invokeRoute(ctx context.Context, tx pgx.Tx, route Route, reqJSON []byte) ([]byte, error) {
	ident := route.Function.Identifier.EscapedString()
	var row pgx.Row
	if len(route.Function.Arguments) > 0 && hasInArgument(route.Function) {
		row = tx.QueryRow(ctx, "select "+ident+"($1::jsonb)", reqJSON)
	} else {
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
