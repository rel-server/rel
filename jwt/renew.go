package jwt

import (
	"maps"
	"net/http"
	"time"

	"github.com/rel-server/rel/config"
)

// ShouldRenew reports whether more than jwt.renewafter of the token's lifespan has elapsed since its iat.
func ShouldRenew(cfg config.Jwt, claims Claims) bool {
	iat := IssuedAt(claims)
	exp := ExpiresAt(claims)
	lifespan := exp.Sub(iat)
	if lifespan <= 0 {
		return false
	}
	elapsed := time.Since(iat)
	return float64(elapsed) > cfg.RenewAfter*float64(lifespan)
}

// Renew re-mints claims with a fresh iat/exp of the same exp-iat width (keeping a jwt_attrs.maxage override),
// every other key copied as-is. The renewed cookie's SameSite reverts to cfg.Jwt.SameSite.
func Renew(cfg config.Jwt, claims Claims) Claims {
	width := ExpiresAt(claims).Sub(IssuedAt(claims))
	now := time.Now().UTC()

	renewed := Claims{}
	maps.Copy(renewed, claims)
	renewed[claimIat] = float64(now.Unix())
	renewed[claimExp] = float64(now.Add(width).Unix())
	return renewed
}

// RenewIfDue renews, signs and writes a fresh Set-Cookie when ShouldRenew says so. A signing failure is
// ignored : a renewal never fails the request. The request keeps being served with the old claims.
func RenewIfDue(cfg config.Jwt, w http.ResponseWriter, claims Claims) {
	if !ShouldRenew(cfg, claims) {
		return
	}
	renewed := Renew(cfg, claims)
	if token, err := Sign(cfg, renewed); err == nil {
		http.SetCookie(w, CookieValue(cfg, token, renewed, ""))
	}
}

// ClearSessionCookie deletes any Set-Cookie header already written to w, then writes the clearing cookie.
func ClearSessionCookie(cfg config.Jwt, w http.ResponseWriter) {
	w.Header().Del("Set-Cookie")
	http.SetCookie(w, ClearCookie(cfg))
}

// ResolveRole is the "role" claim when verified, anonymousRole otherwise.
func ResolveRole(anonymousRole string, claims Claims, verified bool) string {
	if verified {
		return Role(claims)
	}
	return anonymousRole
}
