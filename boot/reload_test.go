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
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/route"
	"github.com/rel-server/rel/server"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestReloader_Reload_EndToEnd applies a fixture migration via Reload and
// confirms a request against the newly-migrated schema succeeds.
func TestReloader_Reload_EndToEnd(t *testing.T) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:16-alpine", postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("starting postgres container: %v", err)
	}
	defer func() { _ = container.Terminate(ctx) }()

	uri, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	// Base schema set up directly, outside reload.cmd : a fixed precondition,
	// not part of what this test is migrating.
	setupPool, err := pg.NewInfos(uri)
	if err != nil {
		t.Fatalf("pg.NewInfos: %v", err)
	}
	if _, err := setupPool.Pool.Exec(ctx, `create role "~anonymous";`); err != nil {
		t.Fatalf("base schema setup: %v", err)
	}
	setupPool.Pool.Close()

	cfg := config.Test()
	cfg.Pg.Query.AnonymousRole = "~anonymous"
	// reload.cmd, not a dmut fixture (specs/reload.md) : psql applying a
	// plain SQL script proves the generic reload.cmd exec path, not any
	// particular migration tool's own mechanics.
	cfg.Reload.Cmd = fmt.Sprintf("psql %q -v ON_ERROR_STOP=1 -f testdata/reload_fixture.sql", uri)
	cfg.Reload.Timeout = 30
	cfg.Reload.DrainTimeout = 5

	db, err := pg.NewInfosAdminQuery(uri, uri, 0, cfg.Pg.Query.AnonymousRole)
	if err != nil {
		t.Fatalf("pg.NewInfosAdminQuery: %v", err)
	}

	reg, err := route.BuildRegistry(db, cfg)
	if err != nil {
		t.Fatalf("route.BuildRegistry: %v", err)
	}

	mux := chi.NewRouter()
	mux.Handle("/rel", server.NewRelHandler(db, cfg, nil))
	route.RegisterRoutes(mux, db, cfg, reg, nil, nil)

	wrapper := NewReloadableHandler(mux)

	// Before the reload : the migration's route function doesn't exist yet.
	reqBefore := httptest.NewRequest("GET", "/created", nil)
	recBefore := httptest.NewRecorder()
	wrapper.ServeHTTP(recBefore, reqBefore)
	if recBefore.Code != http.StatusNotFound {
		t.Fatalf("expected 404 before the reload, got %d (body %q)", recBefore.Code, recBefore.Body.String())
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	reloader := NewReloader(wrapper, db, uri, cfg, logger)
	reloader.Reload(ctx)

	// Registry rebuild picked up the migration's new function.
	reqAfter := httptest.NewRequest("GET", "/created", nil)
	recAfter := httptest.NewRecorder()
	wrapper.ServeHTTP(recAfter, reqAfter)
	if recAfter.Code != http.StatusOK {
		t.Fatalf("expected 200 after the reload, got %d (body %q)", recAfter.Code, recAfter.Body.String())
	}
	if recAfter.Body.String() != "from migration" {
		t.Errorf("expected the migration's own function body, got %q", recAfter.Body.String())
	}

	if reloader.CurrentDbInfos().Pool != db.Pool {
		t.Errorf("expected the reloaded DbInfos to reuse the exact same pool")
	}
}
