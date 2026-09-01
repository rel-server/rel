package jwt

import (
	"maps"
	"net/http"
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

// RenewIfDue is Lifecycle step 4 in full : ShouldRenew's check, Renew
// itself, signing, and writing the fresh Set-Cookie — the exact sequence
// Middleware, rpc/handler.go's handleRpc, and rpc/upload_handler.go's
// handleUploadRoute each ran as their own copy before this was factored
// out. Returns claims unchanged when renewal isn't due, or a signing
// failure is silently ignored (same as before : a renewal is a courtesy,
// not something worth failing the request over) — either way the caller
// always gets back the claims it should keep using for the rest of the
// request.
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

// ClearSessionCookie deletes any Set-Cookie header already written to w and
// writes the clearing cookie in its place — server/rel.go's applyRole and
// /rpc's two check-session-rejection sites all need this exact sequence :
// Header().Del first, since http.SetCookie itself only Adds, and a renewed-
// but-now-rejected session would otherwise leave two Set-Cookie headers (a
// still-valid renewed token, then the clear). Safe to call even where
// nothing has been set yet (the /rpc sites, where renewal always happens
// AFTER check_session) — Del is then just a no-op.
func ClearSessionCookie(cfg config.Jwt, w http.ResponseWriter) {
	w.Header().Del("Set-Cookie")
	http.SetCookie(w, ClearCookie(cfg))
}

// ResolveRole is Lifecycle step 5's role selection : anonymousRole for an
// unverified request, the claims' own "role" key otherwise — the same
// three-line if repeated at every SET LOCAL ROLE call site
// (server/rel.go's applyRole, rpc/handler.go, rpc/upload_handler.go).
func ResolveRole(anonymousRole string, claims Claims, verified bool) string {
	if verified {
		return Role(claims)
	}
	return anonymousRole
}
