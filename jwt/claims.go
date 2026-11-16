// Package jwt mints, signs, verifies and renews sessions, with no database. reqauth adds the role switch.
package jwt

import (
	"maps"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"

	"github.com/rel-server/rel/config"
)

// Claims is the session token's claims, extended with any free-form
// claims a login/callback function adds :
//
//	{ role: string, iat: number, exp: number, auth_time: number, ...free-form }
//
// A map, so added claims survive Renew untouched. iat/exp/auth_time are float64 Unix seconds, the one form both a
// fresh Mint and a parsed token share that jwtlib reads.
type Claims = jwtlib.MapClaims

const (
	claimRole     = "role"
	claimIat      = "iat"
	claimExp      = "exp"
	claimAuthTime = "auth_time"
)

// Role reads the required "role" claim ; "" if absent.
func Role(c Claims) string {
	s, _ := c[claimRole].(string)
	return s
}

// IssuedAt/ExpiresAt/AuthTime read the three rel-computed timestamp claims ; a missing or malformed value reads
// as the zero time.
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

// Mint builds fresh Claims : iat is now, exp is iat+maxage ; authTime is now for a login, the original one on
// Renew. extra adds free-form claims ; its role/iat/exp/auth_time are overwritten.
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
