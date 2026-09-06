package jwt

import (
	"maps"
	"net/http"
	"time"

	"github.com/rel-server/rel/config"
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
// current token's own exp-iat width (preserving a jwt_attrs.maxage
// override across renewals), every other key copied as-is. The renewed
// cookie's SameSite always reverts to cfg.Jwt.SameSite ; it is never
// carried as a claim.
func Renew(cfg config.Jwt, claims Claims) Claims {
	width := ExpiresAt(claims).Sub(IssuedAt(claims))
	now := time.Now().UTC()

	renewed := Claims{}
	maps.Copy(renewed, claims)
	renewed[claimIat] = float64(now.Unix())
	renewed[claimExp] = float64(now.Add(width).Unix())
	return renewed
}

// RenewIfDue is Lifecycle step 4 in full : ShouldRenew's check, then Renew,
// signing, and writing the fresh Set-Cookie. Returns claims unchanged when
// renewal isn't due ; a signing failure is silently ignored — a renewal is
// a courtesy, never worth failing the request over.
func RenewIfDue(cfg config.Jwt, w http.ResponseWriter, claims Claims) Claims {
	if !ShouldRenew(cfg, claims) {
		return claims
	}
	renewed := Renew(cfg, claims)
	if token, err := Sign(cfg, renewed); err == nil {
		http.SetCookie(w, CookieValue(cfg, token, renewed, ""))
	}
	return renewed
}

// ClearSessionCookie deletes any Set-Cookie header already written to w,
// then writes the clearing cookie — http.SetCookie only ever Adds, so
// without the Del a cookie already written this request would linger
// alongside the clear.
func ClearSessionCookie(cfg config.Jwt, w http.ResponseWriter) {
	w.Header().Del("Set-Cookie")
	http.SetCookie(w, ClearCookie(cfg))
}

// ResolveRole is Lifecycle step 5's role selection : anonymousRole for an
// unverified request, the claims' own "role" key otherwise — the same
// three-line if repeated at every SET LOCAL ROLE call site
// (server/rel.go's applyRole, route/handler.go, route/upload_handler.go).
func ResolveRole(anonymousRole string, claims Claims, verified bool) string {
	if verified {
		return Role(claims)
	}
	return anonymousRole
}
