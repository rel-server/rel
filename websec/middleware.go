// Middleware composes ## CORS and ## CSP into one func(http.Handler)
// http.Handler, applied uniformly to /rel, /route, AND /static — same
// func(http.Handler) http.Handler style jwt/middleware.go already uses.
package websec

import (
	"net/http"

	"github.com/ceymard/rel/config"
)

// Middleware generates a fresh nonce for every request (unconditionally —
// ### Nonce : "cheap... so this happens unconditionally"), stashes it in
// the request context, sets the default (process-wide) CSP header BEFORE
// calling next (so a plain-text error response emitted by next still
// carries it, and so a route function's own writeRelHttpResponse can
// overwrite Content-Security-Policy on top when resp.csp is set — see
// route/encode.go), and answers a CORS preflight directly, never reaching
// next at all.
func Middleware(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			nonce, err := NewNonce()
			if err != nil {
				log.Error("websec: generating CSP nonce", "error", err.Error())
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}

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

			w.Header().Set("Content-Security-Policy", Policy(cfg.Http.Csp, "", nonce))

			if h, ok := BuildCorsHeaders(cfg.Http.Cors, origin, false); ok {
				writeCorsHeaders(w, h)
			}

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
