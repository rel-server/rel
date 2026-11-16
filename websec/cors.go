// CORS origin matching and preflight detection.
package websec

import (
	"strconv"
	"strings"

	"github.com/rel-server/rel/config"
)

func maxAgeString(seconds int) string {
	return strconv.Itoa(seconds)
}

// IsPreflight reports whether r is a CORS preflight : an OPTIONS with both Origin and
// Access-Control-Request-Method. Any other OPTIONS goes to normal dispatch, so a fn__options route still works.
func IsPreflight(method, origin, requestMethod string) bool {
	return method == "OPTIONS" && origin != "" && requestMethod != ""
}

// OriginDecision is the result of matching one request's Origin against
// http.cors.allowed_origins.
type OriginDecision struct {
	Allowed         bool
	HeaderValue     string // Access-Control-Allow-Origin's value, when Allowed
	AllowCredential bool   // whether Access-Control-Allow-Credentials: true is also sent
	Vary            bool   // whether Vary: Origin is also sent
}

// ResolveOrigin matches origin : an allowedOrigins of exactly "*" allows every origin, without credentials or
// reflecting it ; otherwise an exact match in the comma-separated list reflects the origin, with credentials
// and Vary: Origin. No match sends no header.
func ResolveOrigin(allowedOrigins string, requestOrigin string) OriginDecision {
	if requestOrigin == "" {
		return OriginDecision{}
	}
	if allowedOrigins == "*" {
		return OriginDecision{Allowed: true, HeaderValue: "*"}
	}
	for _, entry := range strings.Split(allowedOrigins, ",") {
		if strings.TrimSpace(entry) == requestOrigin {
			return OriginDecision{Allowed: true, HeaderValue: requestOrigin, AllowCredential: true, Vary: true}
		}
	}
	return OriginDecision{}
}

// CorsHeaders is the CORS headers to write, on a preflight or an actual response.
type CorsHeaders struct {
	AllowOrigin      string
	AllowCredentials bool
	Vary             bool
	// Preflight-only fields, empty/zero on an actual (non-preflight) response.
	AllowMethods string
	AllowHeaders string
	MaxAge       string
}

// BuildCorsHeaders resolves cfg against requestOrigin, with the preflight-only fields (methods/headers/max-age)
// only when isPreflight.
func BuildCorsHeaders(cfg config.HttpCors, requestOrigin string, isPreflight bool) (CorsHeaders, bool) {
	decision := ResolveOrigin(cfg.AllowedOrigins, requestOrigin)
	if !decision.Allowed {
		return CorsHeaders{}, false
	}
	h := CorsHeaders{
		AllowOrigin:      decision.HeaderValue,
		AllowCredentials: decision.AllowCredential,
		Vary:             decision.Vary,
	}
	if isPreflight {
		h.AllowMethods = cfg.AllowedMethods
		h.AllowHeaders = cfg.AllowedHeaders
		h.MaxAge = maxAgeString(cfg.MaxAge)
	}
	return h, true
}
