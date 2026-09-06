// Copyright 2025 Christophe Eymard
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package boot

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/logging"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/route"
	"github.com/ceymard/rel/server"
	"github.com/ceymard/rel/sso"
	"github.com/ceymard/rel/static"
	"github.com/ceymard/rel/websec"
	"github.com/ceymard/rel/wellknown"
)

// BuildMux assembles the full inner http.Handler — /rel, /route/, and (when
// at least one http.static.path directory exists) /static/ — wrapped
// uniformly in logging.RequestMiddleware (specs/logging.md ## Request-
// scoped logging) and websec.Middleware (specs/http-content.md's "CORS and
// CSP apply to /rel, /route, AND /static uniformly"). /route and /auth
// (the SSO mount) additionally sit behind websec.NonceMiddleware, scoped
// via a chi.Group rather than a path-prefixed sub-router since sso.Mount's
// own route patterns are already absolute (docs/content/http/cors-csp.md
// ## CSP ### Nonce explains why /rel and /static don't get one). A
// well-known query is invoked through /rel, not a separate route ; wkReg
// is handed to server.NewRelHandler directly. Both cmd/rel/main.go
// (startup) and boot/reload.go's Reload (every SIGUSR1) call this with a
// freshly built *route.Registry and *wellknown.Registry —
// registry-building itself stays each call site's own responsibility.
func BuildMux(db *pg.DbInfos, cfg *config.Config, reg *route.Registry, wkReg *wellknown.Registry, logger *slog.Logger) (http.Handler, error) {
	staticSrv := static.New(cfg.Http)

	mux := chi.NewRouter()
	mux.Handle("/rel", server.NewRelHandler(db, cfg, wkReg))
	// specs/typescript.md ## Configuration : gated by http.typescript.enable,
	// not mounted at all otherwise (no 404 handler needed for the disabled case).
	if cfg.Http.TypeScript.Enable {
		mux.Handle("/rel/database.ts", server.NewTypeScriptHandler(db, cfg, wkReg))
	}
	if staticSrv != nil {
		mux.Handle("/static/*", http.StripPrefix("/static/", staticSrv.Handler(db, cfg)))
	} else if logger != nil {
		logger.Debug("boot: no http.static.path directory found, /static/ is not mounted")
	}

	mux.Group(func(r chi.Router) {
		r.Use(websec.NonceMiddleware(cfg))

		r.Handle("/route/*", route.NewHandler(db, cfg, reg, staticSrv))

		// specs/oauth-saml.md : /auth/oidc/* and /auth/saml/* — a no-op when
		// neither openid.* nor saml.* has any entry configured. Mount resolves
		// each entry's own host (http.public_host or its own public_host
		// override) and logs/skips individually, rather than an all-or-nothing
		// gate here.
		sso.Mount(r, db, cfg)
	})

	return logging.RequestMiddleware(websec.Middleware(cfg)(mux)), nil
}
