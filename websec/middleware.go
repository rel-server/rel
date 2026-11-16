package websec

import (
	"net/http"

	"github.com/rel-server/rel/config"
)

// Middleware applies CORS and CSP to every path. It answers a preflight itself, and otherwise sets the base CSP
// header, without nonce, before calling next, so an error response carries it and a route's resp.csp or
// NonceMiddleware can overwrite it.
func Middleware(cfg *config.Config) func(http.Handler) http.Handler {
	// Logged once per boot or reload : this is the policy /rel and /static get.
	log.Info("effective content-security-policy", "policy", Policy(cfg.Http.Csp, "", ""))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			requestMethod := r.Header.Get("Access-Control-Request-Method")

			if IsPreflight(r.Method, origin, requestMethod) {
				// No CSP header : a 204 has no body.
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

// NonceMiddleware puts a fresh nonce in the request context (see NonceFromContext) and re-sets the
// Content-Security-Policy header with it.
//
// Mounted only around /route and /auth, which can render HTML ; /rel and /static have nothing to put a
// nonce in.
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
