package jwt

import (
	"net/http"

	"github.com/rel-server/rel/config"
)

// sameSite maps jwt.samesite to net/http's enum ; an unknown value falls back to Lax.
func sameSite(s string) http.SameSite {
	switch s {
	case "Strict":
		return http.SameSiteStrictMode
	case "None":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}

// CookieValue builds the Set-Cookie for a minted or renewed session ; Max-Age is always the token's exp-iat.
// token is claims, signed.
func CookieValue(cfg config.Jwt, token string, claims Claims, samesiteOverride string) *http.Cookie {
	ss := cfg.SameSite
	if samesiteOverride != "" {
		ss = samesiteOverride
	}
	maxAge := int(ExpiresAt(claims).Sub(IssuedAt(claims)).Seconds())
	return &http.Cookie{
		Name:     cfg.CookieName,
		Value:    token,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: sameSite(ss),
		MaxAge:   maxAge,
	}
}

// ClearCookie is an immediately expiring cookie, for logout (RelHttpResponse.jwt: null) or a check_session
// rejection.
func ClearCookie(cfg config.Jwt) *http.Cookie {
	return &http.Cookie{
		Name:     cfg.CookieName,
		Value:    "",
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: sameSite(cfg.SameSite),
		MaxAge:   -1,
	}
}
