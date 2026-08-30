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
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/rpc"
	"github.com/ceymard/rel/server"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestReloader_Reload_EndToEnd builds a real ReloadableHandler wrapping
// server.NewRelHandler+rpc.NewHandler against a testcontainers postgres,
// applies a fixture migration via a Reload call, and confirms a request
// against the schema the migration just created succeeds where it would
// have 404'd beforehand — the integration test most likely to catch a
// wiring mistake between dmut/pg.ReIntrospect/rpc.BuildRegistry/boot.
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

	// Base schema : the two mimetype/JSON domains route discovery needs,
	// plus the anonymous role — set up directly, outside dmut, since
	// they're a fixed precondition, not part of what this test is
	// migrating.
	setupPool, err := pg.NewInfos(uri)
	if err != nil {
		t.Fatalf("pg.NewInfos: %v", err)
	}
	if _, err := setupPool.Pool.Exec(ctx, `
		create domain "RelHttpRequest" as jsonb;
		create domain "RelHttpResponse" as jsonb;
		create role "~anonymous";
	`); err != nil {
		t.Fatalf("base schema setup: %v", err)
	}
	setupPool.Pool.Close()

	cfg := config.Test()
	cfg.Pg.Query.AnonymousRole = "~anonymous"
	cfg.Dmut.Path = "testdata/reload_fixture"
	cfg.Dmut.ReloadDrainTimeout = 5

	db, err := pg.NewInfosAdminQuery(uri, uri, 0, cfg.Pg.Query.AnonymousRole)
	if err != nil {
		t.Fatalf("pg.NewInfosAdminQuery: %v", err)
	}

	reg, err := rpc.BuildRegistry(db, cfg)
	if err != nil {
		t.Fatalf("rpc.BuildRegistry: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/rel", server.NewRelHandler(db, cfg))
	mux.Handle("/rpc/", rpc.NewHandler(db, cfg, reg, nil))

	wrapper := NewReloadableHandler(mux)

	// Before the reload : the migration's route function doesn't exist yet.
	reqBefore := httptest.NewRequest("GET", "/rpc/public/fn_created_by_migration", nil)
	recBefore := httptest.NewRecorder()
	wrapper.ServeHTTP(recBefore, reqBefore)
	if recBefore.Code != http.StatusNotFound {
		t.Fatalf("expected 404 before the reload, got %d (body %q)", recBefore.Code, recBefore.Body.String())
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	reloader := NewReloader(wrapper, db, uri, cfg, logger)
	reloader.Reload(ctx)

	// After the reload : the migration ran, reintrospection/registry
	// rebuild picked up the new function, and the wrapper is serving the
	// new mux.
	reqAfter := httptest.NewRequest("GET", "/rpc/public/fn_created_by_migration", nil)
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
