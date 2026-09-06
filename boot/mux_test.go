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
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/route"
	"github.com/rel-server/rel/wellknown"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestBuildMux_NonceOnlyOnRouteAndAuth : docs/content/http/cors-csp.md ##
// CSP ### Nonce — /rel never carries a 'nonce-' token (it can never render
// HTML to authorize with one), while /route does (through
// websec.NonceMiddleware, scoped via the chi.Group boot.BuildMux mounts it
// in). Both still carry a Content-Security-Policy header either way — this
// is about the nonce token specifically, not CSP's presence at all.
func TestBuildMux_NonceOnlyOnRouteAndAuth(t *testing.T) {
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

	setupPool, err := pg.NewInfos(uri)
	if err != nil {
		t.Fatalf("pg.NewInfos: %v", err)
	}
	if _, err := setupPool.Pool.Exec(ctx, `
		create domain "RelHttpRequest" as jsonb;
		create domain "RelHttpResponse" as jsonb;
	`); err != nil {
		t.Fatalf("base schema setup: %v", err)
	}
	setupPool.Pool.Close()

	cfg := config.Test()

	db, err := pg.NewInfos(uri)
	if err != nil {
		t.Fatalf("pg.NewInfos: %v", err)
	}

	reg, err := route.BuildRegistry(db, cfg)
	if err != nil {
		t.Fatalf("route.BuildRegistry: %v", err)
	}
	wkReg, err := wellknown.BuildRegistry(db, cfg)
	if err != nil {
		t.Fatalf("wellknown.BuildRegistry: %v", err)
	}

	mux, err := BuildMux(db, cfg, reg, wkReg, nil)
	if err != nil {
		t.Fatalf("BuildMux: %v", err)
	}

	relReq := httptest.NewRequest("POST", "/rel", strings.NewReader(`{"relation":"does_not_exist"}`))
	relReq.Header.Set("Content-Type", "application/json")
	relRec := httptest.NewRecorder()
	mux.ServeHTTP(relRec, relReq)

	relCsp := relRec.Header().Get("Content-Security-Policy")
	if relCsp == "" {
		t.Fatalf("expected /rel to still carry a Content-Security-Policy header, got none")
	}
	if strings.Contains(relCsp, "nonce-") {
		t.Errorf("expected /rel's CSP to carry no nonce token (it can never render HTML), got %q", relCsp)
	}

	routeReq := httptest.NewRequest("GET", "/route/public/does_not_exist", nil)
	routeRec := httptest.NewRecorder()
	mux.ServeHTTP(routeRec, routeReq)

	routeCsp := routeRec.Header().Get("Content-Security-Policy")
	if routeCsp == "" {
		t.Fatalf("expected /route to still carry a Content-Security-Policy header, got none")
	}
	if !strings.Contains(routeCsp, "nonce-") {
		t.Errorf("expected /route's CSP to carry a nonce token, got %q", routeCsp)
	}
}
