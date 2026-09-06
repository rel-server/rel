// Middleware composes ## CORS and ## CSP into one func(http.Handler)
// http.Handler, applied uniformly to /rel, /route, AND /static — same
// func(http.Handler) http.Handler style jwt/middleware.go already uses.
// Nonce generation is a separate, narrower layer — see NonceMiddleware.
package websec

import (
	"net/http"

	"github.com/ceymard/rel/config"
)

// Middleware answers a CORS preflight directly (never reaching next at
// all), and otherwise sets the default (process-wide) CSP header — no
// nonce token in it — BEFORE calling next, so a plain-text error response
// emitted by next still carries it, and so a route function's own
// writeRelHttpResponse can overwrite Content-Security-Policy on top when
// resp.csp is set (see route/encode.go). Mounted around every path
// uniformly ; NonceMiddleware, mounted only around /route and /auth
// (## CSP ### Nonce), overwrites this base header again with a nonce
// appended, for the paths that can actually use one.
func Middleware(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			requestMethod := r.Header.Get("Access-Control-Request-Method")

			if IsPreflight(r.Method, origin, requestMethod) {
				// ### Preflight handling : answered entirely by rel itself.
				// No CSP header here — a 204 has no body for it to govern.
				h, ok := BuildCorsHeaders(cfg.Http.Cors, origin, true)
				if ok {
					writeCorsHeaders(w, h)
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}

			w.Header().Set("Content-Security-Policy", Policy(cfg.Http.Csp, "", ""))

			if h, ok := BuildCorsHeaders(cfg.Http.Cors, origin, false); ok {
				writeCorsHeaders(w, h)
			}

			next.ServeHTTP(w, r)
		})
	}
}

// NonceMiddleware generates a fresh nonce for every request it sees
// (cheap — one crypto/rand read — so this happens unconditionally within
// its scope), stashes it in the request context for a downstream handler
// to read back (RelHttpRequest.csp_nonce, Jet's Nonce variable — see
// NonceFromContext), and re-sets the Content-Security-Policy header
// Middleware already set, this time with the nonce appended.
//
// Mounted only around /route and /auth (SSO callbacks), both of which can
// render HTML through the shared route.WriteRelHttpResponse/Jet-template
// path — never around /rel (JSON only) or /static (files as-is, no
// per-request templating to inject a nonce into), which keep Middleware's
// plain, nonce-less baseline policy instead of paying for a nonce neither
// can ever use (docs/content/http/cors-csp.md ## CSP ### Nonce).
func NonceMiddleware(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			nonce, err := NewNonce()
			if err != nil {
				log.Error("websec: generating CSP nonce", "error", err.Error())
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Security-Policy", Policy(cfg.Http.Csp, "", nonce))

			ctx := WithNonce(r.Context(), nonce)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func writeCorsHeaders(w http.ResponseWriter, h CorsHeaders) {
	if h.AllowOrigin == "" {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", h.AllowOrigin)
	if h.AllowCredentials {
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}
	if h.Vary {
		w.Header().Add("Vary", "Origin")
	}
	if h.AllowMethods != "" {
		w.Header().Set("Access-Control-Allow-Methods", h.AllowMethods)
	}
	if h.AllowHeaders != "" {
		w.Header().Set("Access-Control-Allow-Headers", h.AllowHeaders)
	}
	if h.MaxAge != "" {
		w.Header().Set("Access-Control-Max-Age", h.MaxAge)
	}
}
