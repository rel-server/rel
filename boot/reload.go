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
	"sync"
	"time"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/reloadcmd"
	"github.com/rel-server/rel/route"
	"github.com/rel-server/rel/wellknown"
)

// Reloader owns the mutable state a SIGUSR1 reload (specs/migrations.md ##
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

// Reload runs specs/reload.md's reload sequence : begin maintenance, drain
// in-flight requests, run reload.cmd, reintrospect (skipped when reload.cmd
// failed), rebuild /route + well-known + the TypeScript helper file, swap
// in a fresh mux, resume serving. A reload.cmd failure skips reintrospection
// specifically (specs/reload.md : "the introspection reload is not
// performed, but wellknown and templates still get updated, rebuilt against
// the previous, still-loaded schema") — everything downstream of it still
// runs, against the OLD *pg.DbInfos. Any other step failing (reintrospection
// itself, /route or well-known registry build, mux build) logs and resumes
// serving the OLD handler/schema entirely, never half-swapped and never
// stuck in maintenance mode.
func (rl *Reloader) Reload(ctx context.Context) {
	// New requests stop being served immediately.
	rl.Wrapper.BeginMaintenance()

	// Wait for in-flight requests, bounded by reload.drain_timeout ; past
	// that, their contexts are cancelled.
	rl.Wrapper.Drain(time.Duration(rl.Cfg.Reload.DrainTimeout) * time.Second)

	rl.mu.Lock()
	oldDb := rl.db
	rl.mu.Unlock()

	db := oldDb

	if _, err := reloadcmd.Run(ctx, rl.Cfg, rl.Logger); err != nil {
		rl.Logger.Error("reload: reload.cmd failed, skipping schema reintrospection", "error", err.Error())
	} else {
		// Reintrospect, reusing the EXISTING request-serving pool
		// (pg.ReIntrospect re-checks anonymous-role existence internally).
		newDb, err := pg.ReIntrospect(ctx, rl.PrimaryURI, oldDb.Pool, rl.Cfg.Pg.Query.AnonymousRole)
		if err != nil {
			rl.Logger.Error("reload: reintrospection failed, resuming under the old schema", "error", err.Error())
		} else {
			db = newDb
			if rl.Cfg.Pg.Query.AnonymousRole != "" && !newDb.AnonymousRoleExists {
				rl.Logger.Warn(fmt.Sprintf("configured anonymous role %q does not exist — all anonymous requests will be denied", rl.Cfg.Pg.Query.AnonymousRole))
			}
		}
	}

	// /route and well-known registries rebuild against db (the freshly
	// reintrospected schema, or the old one when reload.cmd/reintrospection
	// didn't succeed) ; either failing logs and resumes under the old schema.
	reg, err := route.BuildRegistry(db, rl.Cfg)
	if err != nil {
		rl.Logger.Error("reload: building /route registry failed, resuming under the old schema", "error", err.Error())
		rl.Wrapper.EndMaintenance()
		return
	}
	wkReg, err := wellknown.BuildRegistry(db, rl.Cfg)
	if err != nil {
		rl.Logger.Error("reload: building well-known query registry failed, resuming under the old schema", "error", err.Error())
		rl.Wrapper.EndMaintenance()
		return
	}

	// specs/typescript.md ## Reloading `helper_path` : right after the
	// registries above, using this same db AND the freshly rebuilt wkReg —
	// Wellknowns generation needs both.
	WriteTypeScriptHelperFile(db, rl.Cfg, wkReg, rl.Logger)

	// A fresh mux (via the same BuildMux startup uses) is stored into the
	// wrapper's atomic.Pointer, never written to Handler directly.
	mux, err := BuildMux(db, rl.Cfg, reg, wkReg, rl.Logger)
	if err != nil {
		rl.Logger.Error("reload: building mux failed, resuming under the old schema", "error", err.Error())
		rl.Wrapper.EndMaintenance()
		return
	}
	rl.Wrapper.Swap(mux)

	rl.mu.Lock()
	rl.db = db
	rl.mu.Unlock()

	rl.Logger.Info("reload: succeeded")

	// New requests resume being served, against the new inner mux.
	rl.Wrapper.EndMaintenance()
}
