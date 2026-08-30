package jwt

import (
	"maps"
	"time"

	"github.com/ceymard/rel/config"
)

// ShouldRenew is Lifecycle step 4's own condition : "more than
// jwt.renewafter of its own lifespan has elapsed since its iat".
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

// Renew re-mints claims per Lifecycle step 4 : fresh iat/exp reusing the
// CURRENT token's own exp-iat width (this is how a jwt_attrs.maxage
// override from the original mint "self-persists" across renewals with no
// extra state — the width itself IS the state), auth_time/role/every other
// key copied as-is. The renewed cookie's SameSite is NOT determined here —
// per this session's resolution, it always reverts to cfg.Jwt.SameSite,
// applied by the caller when building the Set-Cookie (CookieValue with no
// override), not carried as a claim.
func Renew(cfg config.Jwt, claims Claims) Claims {
	width := ExpiresAt(claims).Sub(IssuedAt(claims))
	now := time.Now().UTC()

	renewed := Claims{}
	maps.Copy(renewed, claims)
	renewed[claimIat] = float64(now.Unix())
	renewed[claimExp] = float64(now.Add(width).Unix())
	return renewed
}
