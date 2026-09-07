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

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/route"
	"github.com/rel-server/rel/server"
	"github.com/rel-server/rel/sso"
	"github.com/rel-server/rel/static"
	"github.com/rel-server/rel/websec"
	"github.com/rel-server/rel/wellknown"
)

// BuildMux assembles the full inner http.Handler — /rel, every declared
// route at its own arbitrary path (specs/new-routes.md), and a root-level
// static fallback for any path no declared route claims — wrapped
// uniformly in logging.RequestMiddleware (specs/logging.md ## Request-
// scoped logging) and websec.Middleware (specs/http-content.md's "CORS and
// CSP apply uniformly"). Declared routes and /auth (the SSO mount)
// additionally sit behind websec.NonceMiddleware, scoped via a chi.Group
// (docs/content/http/cors-csp.md ## CSP ### Nonce explains why /rel and the
// static fallback don't get one). A well-known query is invoked through
// /rel, not a separate route ; wkReg is handed to server.NewRelHandler
// directly. Both cmd/rel/main.go (startup) and boot/reload.go's Reload
// (every SIGUSR1) call this with a freshly built *route.Registry and
// *wellknown.Registry — registry-building itself stays each call site's
// own responsibility.
func BuildMux(db *pg.DbInfos, cfg *config.Config, reg *route.Registry, wkReg *wellknown.Registry, logger *slog.Logger) (http.Handler, error) {
	staticSrv := static.New(cfg.Http)
	templates := route.NewTemplateSet(cfg.Http.Templates.Path)

	mux := chi.NewRouter()
	// ## Middleware : "applies uniformly across /rel, since it's served by
	// the same router" — its own dedicated transaction, distinct from
	// /rel's own internal one.
	mux.Handle("/rel", route.GateMiddleware(db, cfg, reg, templates, staticSrv, server.NewRelHandler(db, cfg, wkReg)))
	// specs/typescript.md ## Configuration : gated by http.typescript.enable,
	// not mounted at all otherwise (no 404 handler needed for the disabled case).
	// specs/database-json.md ## Endpoint shares the same gate.
	if cfg.Http.TypeScript.Enable {
		mux.Handle("/rel/database.ts", server.NewTypeScriptHandler(db, cfg, wkReg))
		mux.Handle("/rel/database.json", server.NewDatabaseJSONHandler(db, cfg, wkReg))
	}

	mux.Group(func(r chi.Router) {
		r.Use(websec.NonceMiddleware(cfg))

		route.RegisterRoutes(r, db, cfg, reg, staticSrv)

		// specs/oauth-saml.md : /auth/oidc/* and /auth/saml/* — a no-op when
		// neither openid.* nor saml.* has any entry configured. Mount resolves
		// each entry's own host (http.public_host or its own public_host
		// override) and logs/skips individually, rather than an all-or-nothing
		// gate here.
		sso.Mount(r, db, cfg)
	})

	// specs/new-routes.md ## Static path masking : a path no declared route
	// claims falls through to a static file, if any exist, at the router
	// ROOT — not a fixed /static/* prefix, which is what makes masking "/"
	// (index override) meaningful. An exact-path route always wins first,
	// since chi tries every registered route before NotFound.
	if staticSrv != nil {
		mux.NotFound(route.GateMiddleware(db, cfg, reg, templates, staticSrv, staticSrv.Handler(db, cfg)).ServeHTTP)
	} else if logger != nil {
		logger.Debug("boot: no http.static.path directory found, static fallback is not mounted")
	}

	return logging.RequestMiddleware(websec.Middleware(cfg)(mux)), nil
}
