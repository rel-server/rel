// This file implements specs/http-content.md ## CORS : origin
// matching, the `*` wildcard, and preflight detection — pure logic, no
// http.Handler wiring (that's middleware.go).
package websec

import (
	"strconv"
	"strings"

	"github.com/rel-server/rel/config"
)

func maxAgeString(seconds int) string {
	return strconv.Itoa(seconds)
}

// IsPreflight is ### Preflight handling's own distinguishing rule : "a real
// browser preflight always carries BOTH an Origin header and an
// Access-Control-Request-Method header" — an OPTIONS request missing
// either one is NOT a preflight, and must fall through to normal route
// dispatch instead (so a real fn__options route isn't shadowed).
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

// ResolveOrigin is ## CORS ### Configuration/### `*` as an explicit value's
// matching rule : allowedOrigins == "*" (the WHOLE value, not a comma-list
// containing it) allows every origin but never sends credentials or
// reflects the literal Origin back ; otherwise an exact, comma-separated
// allowlist match reflects the origin AND sends credentials AND Vary :
// Origin (the allowed origin varies per caller). No match at all : not
// allowed, no headers sent.
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

// CorsHeaders is what ResolveOrigin's decision, plus the request's own
// preflight-vs-actual context, translates into actual header writes —
// shared between the preflight responder and the actual-response path
// (### Preflight handling's own "same headers on the real response" rule).
type CorsHeaders struct {
	AllowOrigin      string
	AllowCredentials bool
	Vary             bool
	// Preflight-only fields, empty/zero on an actual (non-preflight) response.
	AllowMethods string
	AllowHeaders string
	MaxAge       string
}

// BuildCorsHeaders resolves cfg against requestOrigin, filling in the
// preflight-only fields (methods/headers/max-age) only when isPreflight —
// ## CORS's actual (non-preflight) response never sends those three, only
// Allow-Origin/-Credentials/Vary.
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
