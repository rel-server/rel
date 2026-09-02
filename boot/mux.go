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

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/logging"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/rpc"
	"github.com/ceymard/rel/server"
	"github.com/ceymard/rel/static"
	"github.com/ceymard/rel/websec"
	"github.com/ceymard/rel/wellknown"
)

// BuildMux assembles the full inner http.Handler — /rel, /rpc/, and (when
// at least one http.static.path directory exists) /static/ — wrapped
// uniformly in logging.RequestMiddleware (specs/logging.md ## Request-
// scoped logging, so every log line any handler emits via
// logging.FromContext(ctx) carries the same request_id) and
// websec.Middleware (specs/http-content.md's own explicit "CORS and CSP
// apply to /rel, /rpc, AND /static uniformly" rule). A well-known query is
// invoked THROUGH /rel, not a separate route — specs/well-known-queries.md
// ## Querying's own history : it stopped being a self-contained invocation
// once WellKnownQuery became just another thing query.ts's Query union
// accepts, same as a Relation, so wkReg is handed to server.NewRelHandler
// directly rather than mounted at its own path. Both cmd/rel/main.go
// (startup) and boot/reload.go's Reload (every SIGUSR1) call this with a
// freshly built *rpc.Registry and *wellknown.Registry, so the two call
// sites cannot drift on what the mux actually contains — registry-building
// itself stays each call site's own responsibility (its failure is handled
// differently : fatal at startup, log-and-keep-old-schema on reload),
// everything about assembling the handler from an already-built registry
// belongs here.
func BuildMux(db *pg.DbInfos, cfg *config.Config, reg *rpc.Registry, wkReg *wellknown.Registry, logger *slog.Logger) (http.Handler, error) {
	staticSrv := static.New(cfg.Http)

	mux := http.NewServeMux()
	mux.Handle("/rel", server.NewRelHandler(db, cfg, wkReg))
	mux.Handle("/rpc/", rpc.NewHandler(db, cfg, reg, staticSrv))
	if staticSrv != nil {
		mux.Handle("/static/", http.StripPrefix("/static/", staticSrv.Handler(db, cfg)))
	} else if logger != nil {
		logger.Debug("boot: no http.static.path directory found, /static/ is not mounted")
	}

	return logging.RequestMiddleware(websec.Middleware(cfg)(mux)), nil
}
