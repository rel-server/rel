package jwt

import (
	"context"
	"net/http"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/logging"
)

// log is this package's own module-tagged logger — specs/logging.md
// ## Domain scoping's convention, one per package.
var log = logging.For("jwt")

// contextKey is unexported so no other package can collide with it by
// constructing an equal-by-value context key.
type contextKey struct{}

// requestSession is what Middleware stashes in the request context : the
// verified claims and whether verification succeeded — see Middleware.
type requestSession struct {
	claims   Claims
	verified bool
}

// Middleware implements Lifecycle step 2 (Verify) only : reads
// cfg.Jwt.CookieName off the request, verifies it (any failure — missing
// cookie, bad signature, expired, session-ceiling exceeded — is "no
// session", never an error in its own right, per step 2), then stashes the
// claims in the request context for the next handler to read via
// FromContext. Verify failures are logged at Debug (reason only, never the
// token) since a forged token and an honestly-expired one are otherwise
// indistinguishable to an operator.
//
// Renew (step 4) is deliberately NOT run here — see requestSession's own
// doc comment : it needs to run AFTER Check (step 3), which needs a DB
// connection Middleware doesn't have, so bundling Renew into this
// DB-independent middleware would put it before Check, the wrong order.
func Middleware(cfg config.Jwt) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, verified := verifyRequest(cfg, r)
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
// Middleware chain can reuse the exact same logic — route/handler.go's own
// handleRoute and the static package's access-control gate both call this
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
		log.Debug("jwt verification failed, treating as anonymous", "path", r.URL.Path, "error", err.Error())
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
