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

// BuildMux assembles /rel, every declared route and a root-level static fallback, all behind the access log,
// CORS/CSP and the decompression of request bodies. Declared routes and /auth also get a CSP nonce ; the static fallback only gets one on a jet render,
// injected by route.NewStaticHandler itself. Well-known queries go through /rel, not a route of their own.
func BuildMux(db *pg.DbInfos, cfg *config.Config, reg *route.Registry, wkReg *wellknown.Registry, logger *slog.Logger) http.Handler {
	staticSrv := static.New(cfg.Http)
	var staticDirs []string
	if staticSrv != nil {
		staticDirs = staticSrv.Dirs
	}
	templates := route.NewTemplateSet(cfg, staticDirs, db, wkReg)

	mux := chi.NewRouter()
	// The gate middleware runs in its own transaction, distinct from /rel's.
	mux.Handle("/rel", route.GateMiddleware(db, cfg, reg, templates, staticSrv, server.NewRelHandler(db, cfg, wkReg)))
	// Not mounted at all when disabled.
	if cfg.Http.TypeScript.Enable {
		mux.Handle("/rel/database.ts", server.NewTypeScriptHandler(db, cfg, wkReg))
		mux.Handle("/rel/database.json", server.NewDatabaseJSONHandler(db, cfg, wkReg))
	}

	mux.Group(func(r chi.Router) {
		r.Use(websec.NonceMiddleware(cfg))

		route.RegisterRoutes(r, db, cfg, reg, staticSrv, wkReg)

		// /auth/oidc/* and /auth/saml/* ; Mount skips misconfigured entries one by one.
		sso.Mount(r, db, cfg, wkReg)
	})

	// A path no declared route claims falls through to a static file at the router root, so a static index can
	// mask "/". Declared routes always win.
	if staticSrv != nil {
		mux.NotFound(route.GateMiddleware(db, cfg, reg, templates, staticSrv, route.NewStaticHandler(db, cfg, staticSrv, templates)).ServeHTTP)
	} else if logger != nil {
		logger.Debug("boot: no http.static.path directory found, static fallback is not mounted")
	}

	return logging.RequestMiddleware(logging.AccessLog(websec.Middleware(cfg)(decodeRequestBody(cfg, mux))))
}
