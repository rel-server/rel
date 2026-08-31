package jwt

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ceymard/rel/config"
)

// contextKey is unexported so no other package can collide with it by
// constructing an equal-by-value context key.
type contextKey struct{}

// requestSession is what Middleware stashes in the request context : the
// (possibly-renewed) claims and whether a session was actually verified —
// rpc.md ## HTTP's "dispatched dynamically... implemented as
// ordinary func(http.Handler) http.Handler middleware" applies only to
// Verify (step 2) and Renew (step 4) here : Check (step 3, the
// check_session function) and Apply role (step 5) both need the request's
// own DB connection/transaction, which doesn't exist yet at the point
// generic middleware runs — those two steps stay the calling handler's own
// responsibility (see rpc/handler.go's handleRpc, and server/rel.go's
// applyRole), immediately after acquiring a connection. This divergence
// from the spec's literal "all four steps are middleware" wording is
// recorded in specs/TODO.md under "SET LOCAL ROLE / auth timing".
type requestSession struct {
	claims   Claims
	verified bool
}

// Middleware implements Lifecycle steps 2 (Verify) and 4 (Renew) : reads
// cfg.Jwt.CookieName off the request, verifies it (any failure — missing
// cookie, bad signature, expired, session-ceiling exceeded — is "no
// session", never an error in its own right, per step 2), renews and writes
// a fresh Set-Cookie if due, then stashes the resulting claims in the
// request context for the next handler to read via FromContext. Verify
// failures are logged at Debug (reason only, never the token) since a
// forged token and an honestly-expired one are otherwise indistinguishable
// to an operator.
func Middleware(cfg config.Jwt) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, verified := verifyRequest(cfg, r)
			if verified && ShouldRenew(cfg, claims) {
				renewed := Renew(cfg, claims)
				if token, err := Sign(cfg, renewed); err == nil {
					http.SetCookie(w, CookieValue(cfg, token, renewed, ""))
				}
				claims = renewed
			}
			ctx := context.WithValue(r.Context(), contextKey{}, requestSession{claims: claims, verified: verified})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func verifyRequest(cfg config.Jwt, r *http.Request) (Claims, bool) {
	return VerifyRequest(cfg, r)
}

// VerifyRequest is Lifecycle step 2 (Verify), exported so any handler that
// needs "is this request anonymous" without going through the full
// Middleware chain can reuse the exact same logic — rpc/handler.go's own
// handleRpc and the static package's access-control gate both call this
// directly rather than duplicating a third copy. Any failure (missing
// cookie, bad signature, expired, session-ceiling exceeded) is "no
// session", never an error in its own right, per step 2.
func VerifyRequest(cfg config.Jwt, r *http.Request) (Claims, bool) {
	cookie, err := r.Cookie(cfg.CookieName)
	if err != nil {
		return nil, false
	}
	claims, err := Verify(cfg, cookie.Value)
	if err != nil {
		slog.Default().Debug("jwt verification failed, treating as anonymous", "path", r.URL.Path, "error", err.Error())
		return nil, false
	}
	return claims, true
}

// FromContext reads back what Middleware stashed — (nil, false) if
// Middleware was never in the chain, or the request carried no verified
// session.
func FromContext(ctx context.Context) (Claims, bool) {
	sess, ok := ctx.Value(contextKey{}).(requestSession)
	if !ok {
		return nil, false
	}
	return sess.claims, sess.verified
}
