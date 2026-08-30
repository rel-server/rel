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
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/dmut"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/rpc"
	"github.com/ceymard/rel/server"
)

// Reloader owns the mutable state a SIGUSR1 reload (specs/03-dmut.md ##
// Reloading) needs across calls : the reload-aware handler wrapper, and
// the *pg.DbInfos currently backing it (so the NEXT reload's ReIntrospect
// reuses this one's own Pool, per step 4). Built once, at startup, right
// after the initial mux is wrapped ; Reload is then called once per
// SIGUSR1, from cmd/rel's persistent signal-handling goroutine.
type Reloader struct {
	Wrapper    *ReloadableHandler
	PrimaryURI string
	Cfg        *config.Config
	Logger     *slog.Logger

	mu sync.Mutex
	db *pg.DbInfos
}

// NewReloader builds a *Reloader around the *pg.DbInfos already produced by
// the initial pg.NewInfosAdminQuery call at startup.
func NewReloader(wrapper *ReloadableHandler, db *pg.DbInfos, primaryURI string, cfg *config.Config, logger *slog.Logger) *Reloader {
	return &Reloader{
		Wrapper:    wrapper,
		PrimaryURI: primaryURI,
		Cfg:        cfg,
		Logger:     logger,
		db:         db,
	}
}

// CurrentDbInfos returns the *pg.DbInfos currently backing the wrapper's
// inner handler — the one whose Pool must be closed at process shutdown.
func (rl *Reloader) CurrentDbInfos() *pg.DbInfos {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.db
}

// Reload runs specs/03-dmut.md ## Reloading's 7 numbered steps exactly.
// Step 3's dmut-failure branch, and the same log-and-continue policy
// extended to a ReIntrospect/BuildRegistry failure (the spec doesn't say
// what happens on THOSE failing mid-reload ; treated identically to a dmut
// failure here — log, never Swap, flip back out of maintenance, resume
// serving the OLD schema/registry, exactly as if the reload had never been
// requested — since there is equally nothing new to pick up in either
// case), both leave the wrapper serving the OLD handler/schema, never
// half-swapped and never stuck in maintenance mode.
func (rl *Reloader) Reload(ctx context.Context) {
	// Step 1 : new requests stop being served immediately.
	rl.Wrapper.BeginMaintenance()

	// Step 2 : wait for in-flight requests, bounded by
	// dmut.reload_drain_timeout ; past that, their contexts are cancelled.
	rl.Wrapper.Drain(time.Duration(rl.Cfg.Dmut.ReloadDrainTimeout) * time.Second)

	// Step 3 : dmut runs again, same non-fatal failure handling as startup.
	if _, err := dmut.Run(ctx, rl.PrimaryURI, rl.Cfg.Dmut, rl.Logger); err != nil {
		rl.Logger.Error("reload: dmut run failed, resuming under the old schema", "error", err.Error())
		rl.Wrapper.EndMaintenance()
		return
	}

	rl.mu.Lock()
	oldDb := rl.db
	rl.mu.Unlock()

	// Step 4 : reintrospect, reusing the EXISTING request-serving pool —
	// including the anonymous-role existence re-check (pg.ReIntrospect
	// does this internally ; see its own doc comment).
	newDb, err := pg.ReIntrospect(ctx, rl.PrimaryURI, oldDb.Pool, rl.Cfg.Pg.Query.AnonymousRole)
	if err != nil {
		rl.Logger.Error("reload: reintrospection failed, resuming under the old schema", "error", err.Error())
		rl.Wrapper.EndMaintenance()
		return
	}
	if rl.Cfg.Pg.Query.AnonymousRole != "" && !newDb.AnonymousRoleExists {
		rl.Logger.Warn(fmt.Sprintf("configured anonymous role %q does not exist — all anonymous requests will be denied", rl.Cfg.Pg.Query.AnonymousRole))
	}

	// Step 5 : the /rpc registry is rebuilt from the new schema.
	reg, err := rpc.BuildRegistry(newDb, rl.Cfg)
	if err != nil {
		rl.Logger.Error("reload: building /rpc route registry failed, resuming under the old schema", "error", err.Error())
		rl.Wrapper.EndMaintenance()
		return
	}

	// Step 6 : a fresh inner mux is built and stored into the wrapper's
	// atomic.Pointer — a single pointer store, never a write to
	// http.Server.Handler itself.
	mux := http.NewServeMux()
	mux.Handle("/rel", server.NewRelHandler(newDb, rl.Cfg))
	mux.Handle("/rpc/", rpc.NewHandler(newDb, rl.Cfg, reg))
	rl.Wrapper.Swap(mux)

	rl.mu.Lock()
	rl.db = newDb
	rl.mu.Unlock()

	rl.Logger.Info("reload: dmut + reintrospection succeeded, serving the new schema")

	// Step 7 : new requests resume being served, against the new inner mux.
	rl.Wrapper.EndMaintenance()
}
