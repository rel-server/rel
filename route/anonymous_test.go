package route

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// An anonymous route needs an explicit EXECUTE grant to the anonymous role plus schema USAGE ; an EXECUTE it only
// gets through PUBLIC does not count, and every route PUBLIC can execute is warned about. Introspection runs as a
// non-superuser login.
func TestBuildRegistry_AnonymousAuthorizationNeedsAnExplicitGrant(t *testing.T) {
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithInitScripts("../pg/testdata/anon_priv.sql"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	uri := fmt.Sprintf("postgres://plain_login_role:test-password@%s:%s/postgres?sslmode=disable", host, port.Port())

	build := func(t *testing.T, anonRole string) (map[string]bool, map[string]bool) {
		t.Helper()
		cfg := config.Test()
		cfg.Pg.Query.AnonymousRole = anonRole
		cfg.Route = map[string]map[string]config.RouteDecl{
			"locked_schema":       {"fn_locked": {Path: "/locked"}},
			"open_schema":         {"fn_open": {Path: "/open"}},
			"default_only_schema": {"fn_default_only": {Path: "/default-only"}},
		}
		db, err := pg.NewInfosAdminQuery(uri, uri, 0, anonRole)
		if err != nil {
			t.Fatalf("NewInfosAdminQuery: %v", err)
		}
		t.Cleanup(db.Pool.Close)

		var reg *Registry
		lines := captureLogsAt(t, slog.LevelWarn, func() {
			reg, err = BuildRegistry(db, cfg)
		})
		if err != nil {
			t.Fatalf("BuildRegistry: %v", err)
		}
		anon := map[string]bool{}
		for _, r := range reg.Routes {
			anon[r.Function.Identifier.String()] = r.AnonymousAuthorized
		}
		if len(anon) != 3 {
			t.Fatalf("expected the 3 fixture routes, got %v", anon)
		}
		public := map[string]bool{}
		for _, l := range lines {
			if l["msg"] == "route: route is executable by PUBLIC" {
				public[fmt.Sprint(l["target"])] = true
			}
		}
		return anon, public
	}

	anon, public := build(t, "probe_role")
	for fn, want := range map[string]bool{
		"locked_schema.fn_locked":             false, // EXECUTE via PUBLIC, but no schema USAGE
		"open_schema.fn_open":                 true,  // explicit USAGE and EXECUTE
		"default_only_schema.fn_default_only": false, // callable, but only through PUBLIC
	} {
		if anon[fn] != want {
			t.Errorf("%s: AnonymousAuthorized = %v, want %v", fn, anon[fn], want)
		}
	}
	if len(public) != 1 || !public["default_only_schema.fn_default_only"] {
		t.Errorf("expected a PUBLIC warning for default_only_schema.fn_default_only only, got %v", public)
	}

	anon, _ = build(t, "plain_login_role")
	if anon["open_schema.fn_open"] {
		t.Errorf("open_schema.fn_open: a role with no grants must not be anonymously authorized")
	}
}
