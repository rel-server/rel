package jwt

import (
	"net/http"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/logging"
)

var log = logging.For("jwt")

// VerifyRequest reads the session cookie off the request and verifies it. Any failure (missing cookie, bad
// signature, expired, session ceiling exceeded) is no session, never an error.
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
