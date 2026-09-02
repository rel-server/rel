// Package jwt implements specs/authentication.md's "# JWT" claims/
// sign/verify/renew mechanics, plus the Verify/Renew half of the HTTP
// Lifecycle as genuine net/http middleware (middleware.go) : Verify and
// Renew need no database, so they live here as ordinary
// func(http.Handler) http.Handler middleware. Check (the check_session
// function) and Apply role both need the request's own DB connection,
// which doesn't exist yet when this middleware runs — those two steps are
// each calling package's own responsibility (route/handler.go, server/rel.go
// applyRole), using dbauth for the parts they still share.
package jwt

import (
	"maps"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"

	"github.com/ceymard/rel/config"
)

// Claims is specs/authentication.md ## Claims :
//
//	{ role: string, iat: number, exp: number, auth_time: number, ...free-form }
//
// Aliased directly to jwtlib.MapClaims (already map[string]any) rather than
// a fixed struct : "anything else is a free-form claim the developer can
// add" needs to round-trip untouched through Renew ("other claims carried
// over as-is") with no separate bag to shuttle them through — the map IS
// the free-form storage. iat/exp/auth_time are stored as float64 Unix
// seconds : jwtlib.MapClaims's own GetExpirationTime/GetIssuedAt only
// accept float64 or json.Number for a numeric-date claim (see its source),
// and encoding/json round-trips a Go float64 through JSON and back as
// float64 again, so this is the one representation that survives both a
// fresh in-memory Mint AND a Verify of an already-serialized token.
type Claims = jwtlib.MapClaims

const (
	claimRole     = "role"
	claimIat      = "iat"
	claimExp      = "exp"
	claimAuthTime = "auth_time"
)

// Role reads the required "role" claim — empty string if absent (callers
// verifying a token check this against "" to detect a missing role, per
// ## Claims : "Required; a JWT without one is an error").
func Role(c Claims) string {
	s, _ := c[claimRole].(string)
	return s
}

// IssuedAt/ExpiresAt/AuthTime read the three rel-computed timestamp claims.
// A missing/malformed value reads as the zero time — callers that need to
// distinguish "absent" from "zero" don't apply here, since Mint/Renew
// always set all three.
func IssuedAt(c Claims) time.Time  { return timeClaim(c, claimIat) }
func ExpiresAt(c Claims) time.Time { return timeClaim(c, claimExp) }
func AuthTime(c Claims) time.Time  { return timeClaim(c, claimAuthTime) }

func timeClaim(c Claims, key string) time.Time {
	f, ok := c[key].(float64)
	if !ok {
		return time.Time{}
	}
	return time.Unix(int64(f), 0).UTC()
}

// Mint builds a fresh Claims map per Lifecycle step 1 : role and authTime
// are the caller's to decide (a genuinely fresh login uses now ; Renew
// passes the ORIGINAL auth_time through unchanged — see renew.go), iat is
// always now, exp is iat+maxage. extra is copied in as additional free-form
// claims (a login function's own custom claims) ; role/iat/exp/auth_time in
// extra, if present, are overwritten by this function's own values, never
// the other way around — ## Claims : "the supplied values are ignored and
// overwritten" for the three rel-computed timestamps, and role is this
// function's own required parameter, not something extra should also carry.
func Mint(cfg config.Jwt, role string, authTime time.Time, maxage int, extra map[string]any) Claims {
	now := time.Now().UTC()
	c := Claims{}
	maps.Copy(c, extra)
	c[claimRole] = role
	c[claimIat] = float64(now.Unix())
	c[claimExp] = float64(now.Add(time.Duration(maxage) * time.Second).Unix())
	c[claimAuthTime] = float64(authTime.Unix())
	return c
}
