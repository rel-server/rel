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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rel-server/rel/config"
	jwtpkg "github.com/rel-server/rel/jwt"
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
		create function fn_nonce_test() returns text language sql as $$ select 'ok'::text; $$;
		comment on function fn_nonce_test() is 'route:: path: "/testroute"';
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

	routeReq := httptest.NewRequest("GET", "/testroute", nil)
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

// TestBuildMux_StaticMaskingAndRootFallback proves specs/new-routes.md ##
// Static path masking end to end through the real router : a declared route
// at an exact path wins over a same-path static file (chi tries every
// registered route before falling to NotFound), a declared route at "/"
// overrides index serving, and an unmasked static file still serves through
// the root-level NotFound fallback (not a fixed /static/* prefix).
func TestBuildMux_StaticMaskingAndRootFallback(t *testing.T) {
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
		create role "~anonymous";

		create function fn_mask_masked() returns text language sql as $$ select 'ROUTE WINS'::text; $$;
		comment on function fn_mask_masked() is 'route:: path: "/masked.txt"';

		create function fn_mask_root() returns text language sql as $$ select 'ROUTE INDEX'::text; $$;
		comment on function fn_mask_root() is 'route:: path: "/"';

		grant execute on function fn_mask_masked() to "~anonymous";
		grant execute on function fn_mask_root() to "~anonymous";
	`); err != nil {
		t.Fatalf("base schema setup: %v", err)
	}
	setupPool.Pool.Close()

	staticDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("STATIC INDEX"), 0o644); err != nil {
		t.Fatalf("writing index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "masked.txt"), []byte("STATIC MASKED"), 0o644); err != nil {
		t.Fatalf("writing masked.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "other.txt"), []byte("STATIC OTHER"), 0o644); err != nil {
		t.Fatalf("writing other.txt: %v", err)
	}

	cfg := config.Test()
	cfg.Http.Static.Path = staticDir
	cfg.Pg.Query.AnonymousRole = "~anonymous"

	db, err := pg.NewInfosAdminQuery(uri, uri, 0, cfg.Pg.Query.AnonymousRole)
	if err != nil {
		t.Fatalf("pg.NewInfosAdminQuery: %v", err)
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

	// An exact-path route always wins over a same-path static file.
	maskedReq := httptest.NewRequest("GET", "/masked.txt", nil)
	maskedRec := httptest.NewRecorder()
	mux.ServeHTTP(maskedRec, maskedReq)
	if maskedRec.Code != 200 || maskedRec.Body.String() != "ROUTE WINS" {
		t.Errorf("expected the route to mask the static file, got %d: %q", maskedRec.Code, maskedRec.Body.String())
	}

	// A route declared at "/" overrides index serving.
	rootReq := httptest.NewRequest("GET", "/", nil)
	rootRec := httptest.NewRecorder()
	mux.ServeHTTP(rootRec, rootReq)
	if rootRec.Code != 200 || rootRec.Body.String() != "ROUTE INDEX" {
		t.Errorf("expected the route to override index serving, got %d: %q", rootRec.Code, rootRec.Body.String())
	}

	// An unmasked file still serves through the root-level NotFound fallback.
	otherReq := httptest.NewRequest("GET", "/other.txt", nil)
	otherRec := httptest.NewRecorder()
	mux.ServeHTTP(otherRec, otherReq)
	if otherRec.Code != 200 || otherRec.Body.String() != "STATIC OTHER" {
		t.Errorf("expected the unmasked static file to serve, got %d: %q", otherRec.Code, otherRec.Body.String())
	}
}

// TestBuildMux_ChiPrecedence_StaticSegmentBeatsParam proves the plan's
// trusted-but-unverified assumption that chi's own trie precedence (static
// segment > named param) implements specs/new-routes.md's "longest
// anonymized path first" rule, for two routes that would otherwise collide
// on a shared prefix.
func TestBuildMux_ChiPrecedence_StaticSegmentBeatsParam(t *testing.T) {
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
		create role "~anonymous";

		create function fn_prec_special() returns text language sql as $$ select 'SPECIAL'::text; $$;
		comment on function fn_prec_special() is 'route:: path: "/items/special"';

		create function fn_prec_byid(id text) returns text language sql as $$ select 'ITEM ' || id; $$;
		comment on function fn_prec_byid(text) is 'route:: path: "/items/{id}"';

		grant execute on function fn_prec_special() to "~anonymous";
		grant execute on function fn_prec_byid(text) to "~anonymous";
	`); err != nil {
		t.Fatalf("base schema setup: %v", err)
	}
	setupPool.Pool.Close()

	cfg := config.Test()
	cfg.Pg.Query.AnonymousRole = "~anonymous"

	db, err := pg.NewInfosAdminQuery(uri, uri, 0, cfg.Pg.Query.AnonymousRole)
	if err != nil {
		t.Fatalf("pg.NewInfosAdminQuery: %v", err)
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

	specialReq := httptest.NewRequest("GET", "/items/special", nil)
	specialRec := httptest.NewRecorder()
	mux.ServeHTTP(specialRec, specialReq)
	if specialRec.Code != 200 || specialRec.Body.String() != "SPECIAL" {
		t.Errorf("expected the static-segment route to win over the param route, got %d: %q", specialRec.Code, specialRec.Body.String())
	}

	otherReq := httptest.NewRequest("GET", "/items/42", nil)
	otherRec := httptest.NewRecorder()
	mux.ServeHTTP(otherRec, otherReq)
	if otherRec.Code != 200 || otherRec.Body.String() != "ITEM 42" {
		t.Errorf("expected the param route to still match other values, got %d: %q", otherRec.Code, otherRec.Body.String())
	}
}

// TestBuildMux_MiddlewareGatesRel proves specs/new-routes.md ## Middleware
// applies to /rel, replacing check_session's own reject-and-clear-cookie
// pattern : a middleware declared at "/" ("/rel" itself is a reserved path,
// never available for a user declaration — see isReservedPath) terminates
// with {status: 401, jwt: null} when a table flag is set, exactly the
// pattern ## Middleware documents as check_session's replacement.
func TestBuildMux_MiddlewareGatesRel(t *testing.T) {
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
		create role "~anonymous";
		create role authenticated_user;
		grant authenticated_user to current_user;

		create table session_gate (id serial primary key, reject boolean not null default false);
		insert into session_gate (reject) values (false);
		grant select on session_gate to authenticated_user, "~anonymous";

		create function relgate_probe() returns jsonb language sql as $$ select '{"ok":true}'::jsonb; $$;
		grant execute on function relgate_probe() to authenticated_user;

		create function fn_rel_gate(req jsonb, out resp jsonb, out content jsonb) returns record language sql as $$
			select
				case when (select reject from session_gate limit 1)
					then jsonb_build_object('status', 401, 'jwt', 'null'::jsonb)
				end,
				null::jsonb;
		$$;
		comment on function fn_rel_gate(jsonb) is 'route:: path: "/", middleware: true';
		grant execute on function fn_rel_gate(jsonb) to "~anonymous", authenticated_user;
	`); err != nil {
		t.Fatalf("base schema setup: %v", err)
	}
	setupPool.Pool.Close()

	cfg := config.Test()
	cfg.Pg.Query.AnonymousRole = "~anonymous"
	cfg.Dev = true

	db, err := pg.NewInfosAdminQuery(uri, uri, 0, cfg.Pg.Query.AnonymousRole)
	if err != nil {
		t.Fatalf("pg.NewInfosAdminQuery: %v", err)
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

	claims := jwtpkg.Mint(cfg.Jwt, "authenticated_user", time.Now(), cfg.Jwt.MaxAge, nil)
	token, err := jwtpkg.Sign(cfg.Jwt, claims)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	cookie := jwtpkg.CookieValue(cfg.Jwt, token, claims, "")

	postRel := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/rel", strings.NewReader(`{"function":"relgate_probe","schema":"public"}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := postRel(); rec.Code != http.StatusOK {
		t.Fatalf("expected 200 before the gate trips, got %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := db.Pool.Exec(ctx, "update session_gate set reject = true"); err != nil {
		t.Fatalf("tripping the gate: %v", err)
	}

	rec := postRel()
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 from the middleware, got %d: %s", rec.Code, rec.Body.String())
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == cfg.Jwt.CookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("expected the jwt cookie to be cleared, got %v", rec.Result().Cookies())
	}
}

// TestBuildMux_MiddlewareRejectionDoesNotAlsoRenew is a regression test :
// jwt/middleware.go's own doc comment states Renew must run AFTER Check.
// A token past jwt.renew_after AND rejected by the gating middleware must
// leave exactly one Set-Cookie (the clear) — a renewed-then-cleared pair
// would hand the client a working cookie for the very session {status:401,
// jwt:null} just rejected.
func TestBuildMux_MiddlewareRejectionDoesNotAlsoRenew(t *testing.T) {
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
		create role "~anonymous";
		create role authenticated_user;
		grant authenticated_user to current_user;

		create function fn_rel_gate_always(req jsonb, out resp jsonb, out content jsonb) returns record language sql as $$
			select jsonb_build_object('status', 401, 'jwt', 'null'::jsonb), null::jsonb;
		$$;
		comment on function fn_rel_gate_always(jsonb) is 'route:: path: "/", middleware: true';
		grant execute on function fn_rel_gate_always(jsonb) to "~anonymous", authenticated_user;
	`); err != nil {
		t.Fatalf("base schema setup: %v", err)
	}
	setupPool.Pool.Close()

	cfg := config.Test()
	cfg.Pg.Query.AnonymousRole = "~anonymous"
	cfg.Jwt.MaxAge = 5
	cfg.Jwt.RenewAfter = 0.1

	db, err := pg.NewInfosAdminQuery(uri, uri, 0, cfg.Pg.Query.AnonymousRole)
	if err != nil {
		t.Fatalf("pg.NewInfosAdminQuery: %v", err)
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

	claims := jwtpkg.Mint(cfg.Jwt, "authenticated_user", time.Now(), cfg.Jwt.MaxAge, nil)
	token, err := jwtpkg.Sign(cfg.Jwt, claims)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	cookie := jwtpkg.CookieValue(cfg.Jwt, token, claims, "")
	time.Sleep(1500 * time.Millisecond) // cross the renew_after threshold

	// The body never gets read — the gating middleware always terminates
	// before /rel's own handler runs at all.
	req := httptest.NewRequest(http.MethodPost, "/rel", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 from the middleware, got %d: %s", rec.Code, rec.Body.String())
	}
	setCookies := rec.Result().Cookies()
	if len(setCookies) != 1 {
		t.Fatalf("expected exactly 1 Set-Cookie, got %d: %v", len(setCookies), setCookies)
	}
	if setCookies[0].MaxAge >= 0 {
		t.Errorf("expected the one Set-Cookie to be a clear (negative MaxAge), got %+v", setCookies[0])
	}
}

// TestBuildMux_MiddlewareGatesStaticFallback proves specs/new-routes.md
// ## Middleware supersedes http.static.access.* : a middleware declared at
// a prefix under http.static.path gates a static file under that prefix
// exactly the way the old named access-control rule did.
func TestBuildMux_MiddlewareGatesStaticFallback(t *testing.T) {
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
		create role "~anonymous";

		create function fn_static_gate(req jsonb, out resp jsonb, out content jsonb) returns record language sql as $$
			select jsonb_build_object('status', 403), jsonb_build_object('reason', 'forbidden');
		$$;
		comment on function fn_static_gate(jsonb) is 'route:: path: "/private", middleware: true';
		grant execute on function fn_static_gate(jsonb) to "~anonymous";
	`); err != nil {
		t.Fatalf("base schema setup: %v", err)
	}
	setupPool.Pool.Close()

	staticDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(staticDir, "private"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "private", "secret.txt"), []byte("TOP SECRET"), 0o644); err != nil {
		t.Fatalf("writing secret.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "public.txt"), []byte("PUBLIC"), 0o644); err != nil {
		t.Fatalf("writing public.txt: %v", err)
	}

	cfg := config.Test()
	cfg.Http.Static.Path = staticDir
	cfg.Pg.Query.AnonymousRole = "~anonymous"

	db, err := pg.NewInfosAdminQuery(uri, uri, 0, cfg.Pg.Query.AnonymousRole)
	if err != nil {
		t.Fatalf("pg.NewInfosAdminQuery: %v", err)
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

	gatedReq := httptest.NewRequest("GET", "/private/secret.txt", nil)
	gatedRec := httptest.NewRecorder()
	mux.ServeHTTP(gatedRec, gatedReq)
	if gatedRec.Code != http.StatusForbidden {
		t.Errorf("expected 403 from the middleware gating /private, got %d: %s", gatedRec.Code, gatedRec.Body.String())
	}

	openReq := httptest.NewRequest("GET", "/public.txt", nil)
	openRec := httptest.NewRecorder()
	mux.ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusOK || openRec.Body.String() != "PUBLIC" {
		t.Errorf("expected the ungated file to serve normally, got %d: %q", openRec.Code, openRec.Body.String())
	}
}
