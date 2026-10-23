package jwt

import (
	"net/http"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/logging"
)

// log is this package's own module-tagged logger — specs/logging.md
// ## Domain scoping's convention, one per package.
var log = logging.For("jwt")

// VerifyRequest is Lifecycle step 2 (Verify) : reads cfg.CookieName off the
// request and verifies it. The reqauth package builds a request's user from
// it ; the SSO callback and static pages call it directly. Any failure (missing
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
