package jwt

import (
	"fmt"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"

	"github.com/rel-server/rel/config"
)

// signingMethod maps jwt.algorithm's three accepted values to the
// library's SigningMethod ; anything else errors rather than panics.
func signingMethod(algorithm string) (*jwtlib.SigningMethodHMAC, error) {
	switch algorithm {
	case "HS256":
		return jwtlib.SigningMethodHS256, nil
	case "HS384":
		return jwtlib.SigningMethodHS384, nil
	case "HS512":
		return jwtlib.SigningMethodHS512, nil
	default:
		return nil, fmt.Errorf("jwt: unrecognized jwt.algorithm %q (want HS256/HS384/HS512)", algorithm)
	}
}

// Sign produces a signed token string for claims, using cfg.Jwt.Algorithm/
// Secret.
func Sign(cfg config.Jwt, claims Claims) (string, error) {
	method, err := signingMethod(cfg.Algorithm)
	if err != nil {
		return "", err
	}
	token := jwtlib.NewWithClaims(method, claims)
	return token.SignedString([]byte(cfg.Secret))
}

// Verify parses and validates token per Lifecycle step 2 : signature,
// jwt.algorithm (via WithValidMethods — rejects any token whose header
// claims a different algorithm, including "none", exactly as the spec
// requires), and exp (the library's own default validation, since exp is
// always present on a rel-minted token). auth_time + jwt.maxsessionage is
// NOT a standard JWT claim the library validates on its own, so it's
// checked separately here.
//
// A verification failure of ANY kind returns a plain error — the caller
// (route package) treats that as "no session", per Lifecycle step 2 : "Failing
// any of these is equivalent to no session at all", never a request-level
// error in its own right.
func Verify(cfg config.Jwt, token string) (Claims, error) {
	claims := Claims{}
	_, err := jwtlib.ParseWithClaims(token, claims, func(t *jwtlib.Token) (any, error) {
		return []byte(cfg.Secret), nil
	}, jwtlib.WithValidMethods([]string{cfg.Algorithm}))
	if err != nil {
		return nil, fmt.Errorf("jwt: verify: %w", err)
	}

	authTime := AuthTime(claims)
	if authTime.IsZero() {
		return nil, fmt.Errorf("jwt: verify: missing auth_time claim")
	}
	ceiling := authTime.Add(time.Duration(cfg.MaxSessionAge) * time.Second)
	if time.Now().After(ceiling) {
		return nil, fmt.Errorf("jwt: verify: session exceeds jwt.maxsessionage")
	}
	if Role(claims) == "" {
		return nil, fmt.Errorf("jwt: verify: missing role claim")
	}

	return claims, nil
}
