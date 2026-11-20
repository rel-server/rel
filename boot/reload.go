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
	"log/slog"
	"sync"
	"time"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/reloadcmd"
)

// Reloader holds what is kept across SIGUSR1 reloads : the handler wrapper and the *pg.DbInfos backing it, whose
// Pool the next reload reuses. Built once at startup.
type Reloader struct {
	Wrapper    *ReloadableHandler
	PrimaryURI string
	Cfg        *config.Config
	Logger     *slog.Logger

	mu sync.Mutex
	db *pg.DbInfos
}

// NewReloader builds a *Reloader around the startup *pg.DbInfos.
func NewReloader(wrapper *ReloadableHandler, db *pg.DbInfos, primaryURI string, cfg *config.Config, logger *slog.Logger) *Reloader {
	return &Reloader{
		Wrapper:    wrapper,
		PrimaryURI: primaryURI,
		Cfg:        cfg,
		Logger:     logger,
		db:         db,
	}
}

// CurrentDbInfos returns the *pg.DbInfos currently served, whose Pool must be closed at shutdown.
func (rl *Reloader) CurrentDbInfos() *pg.DbInfos {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.db
}

// Reload enters maintenance, drains in-flight requests, closes the pooled connections, runs reload.cmd, reintrospects, builds a new snapshot and
// swaps it in. A reload.cmd failure only skips reintrospection : the snapshot is still rebuilt against the old
// schema. Any other failure logs and keeps serving the old snapshot. Maintenance always ends.
func (rl *Reloader) Reload(ctx context.Context) {
	rl.Wrapper.BeginMaintenance()
	defer rl.Wrapper.EndMaintenance()

	// Past reload.drain_timeout, in-flight requests' contexts are cancelled.
	rl.Wrapper.Drain(time.Duration(rl.Cfg.Reload.DrainTimeout) * time.Second)

	rl.mu.Lock()
	oldDb := rl.db
	rl.mu.Unlock()

	db := oldDb

	// A pooled connection keeps the temp tables writes create, typed like their relations : they would block reload.cmd
	// from dropping a type they use, and keep an old layout afterwards. A connection still checked out past the drain is
	// closed when released.
	oldDb.Pool.Reset()

	if _, err := reloadcmd.Run(ctx, rl.Cfg, slog.Default()); err != nil {
		rl.Logger.Error("reload: reload.cmd or dmut failed, skipping schema reintrospection", reloadcmd.ErrorAttrs(err)...)
	} else {
		// Reuses the request-serving pool.
		newDb, err := pg.ReIntrospect(ctx, rl.PrimaryURI, oldDb.Pool, rl.Cfg.Pg.Query.AnonymousRole, pg.WithNoticeLevel(rl.Cfg.Pg.NoticesLevel))
		if err != nil {
			rl.Logger.Error("reload: reintrospection failed, resuming under the old schema", "error", err.Error())
		} else {
			db = newDb
			warnMissingAnonymousRole(db, rl.Cfg, rl.Logger)
		}
	}

	snap, err := Build(db, rl.Cfg, rl.Logger)
	if err != nil {
		rl.Logger.Error("reload: building the new snapshot failed, resuming under the old schema", "error", err.Error())
		return
	}
	rl.Wrapper.Swap(snap.Handler)

	rl.mu.Lock()
	rl.db = db
	rl.mu.Unlock()

	// Only once the new snapshot is the one served.
	snap.WriteTypeScriptHelper(rl.Cfg, rl.Logger)

	rl.Logger.Info("reload: succeeded")
}
