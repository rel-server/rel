package jwt

import (
	"net/http"

	"github.com/ceymard/rel/config"
)

// sameSite maps jwt.samesite's string form to net/http's enum — an
// unrecognized value falls back to Lax rather than erroring per-request ;
// cfg.Jwt.SameSite is validated once at startup (config assembly), not
// re-validated on every cookie write.
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

// CookieValue builds the Set-Cookie for a (freshly minted or renewed)
// session : Max-Age always mirrors the token's own exp-iat, per Lifecycle's
// "The JWT cookie's Max-Age always mirrors the token's own exp - iat — it
// is never set independently." token is claims already Sign-ed.
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

// ClearCookie is logout (RelHttpResponse.jwt: null) or a check_session
// rejection — an immediately-expiring cookie, forcing re-authentication.
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
