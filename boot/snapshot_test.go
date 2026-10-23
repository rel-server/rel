package boot

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
)

// snapshotFixture is a schema with one anonymous route, so a build has something to check
// privileges for.
const snapshotFixture = `
create role "~anonymous";
create function fn_hello() returns text language sql as $$ select 'hello'::text $$;
comment on function fn_hello() is 'route:: path: "/hello"';
grant execute on function fn_hello() to "~anonymous";
`

// snapshotConfig starts Postgres with snapshotFixture and returns a config that reaches it, with
// a TypeScript helper file under t.TempDir().
func snapshotConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine", postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("starting postgres container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	uri, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	setup, err := pg.NewInfos(uri)
	if err != nil {
		t.Fatalf("pg.NewInfos: %v", err)
	}
	if _, err := setup.Pool.Exec(ctx, snapshotFixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	setup.Pool.Close()

	cfg := config.Test()
	cfg.Pg.Query.AnonymousRole = "~anonymous"
	cfg.Pg.URI, cfg.Pg.User, cfg.Pg.Password = uri, "postgres", "postgres"
	cfg.Pg.ConnectTimeout = 10
	cfg.Reload.Timeout = 30
	cfg.Reload.DrainTimeout = 5
	helper := filepath.Join(t.TempDir(), "database.ts")
	cfg.TypeScript.HelperPath = helper
	return cfg, helper
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestStart_ServesTheFirstSnapshotAndWritesTheHelperFile(t *testing.T) {
	cfg, helper := snapshotConfig(t)
	reloader, err := Start(context.Background(), cfg, testLogger())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer reloader.CurrentDbInfos().Pool.Close()

	if rec := get(reloader.Wrapper, "/hello"); rec.Code != http.StatusOK || rec.Body.String() != "hello" {
		t.Errorf("expected 200 hello, got %d %q", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(helper); err != nil {
		t.Errorf("expected the helper file to be written at startup: %v", err)
	}
}

func TestStart_UnreachableDatabaseIsAnError(t *testing.T) {
	cfg := config.Test()
	cfg.Pg.URI = "postgres://u:p@127.0.0.1:1/db?sslmode=disable"
	cfg.Pg.ConnectTimeout = 1

	start := time.Now()
	reloader, err := Start(context.Background(), cfg, testLogger())
	if err == nil {
		reloader.CurrentDbInfos().Pool.Close()
		t.Fatal("expected an error")
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("Start should give up after pg.connect_timeout, took %v", time.Since(start))
	}
}

func TestBuild_FailsWhenTheDatabaseCannotBeAsked(t *testing.T) {
	cfg, _ := snapshotConfig(t)
	db, err := pg.NewInfosAdminQuery(cfg.Pg.URI, cfg.Pg.URI, 0, cfg.Pg.Query.AnonymousRole)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(db, cfg, testLogger()); err != nil {
		t.Fatalf("Build on a live pool: %v", err)
	}
	db.Pool.Close()
	if snap, err := Build(db, cfg, testLogger()); err == nil {
		t.Fatalf("expected an error on a closed pool, got %+v", snap)
	}
}

// A reload whose build fails keeps serving the old snapshot : same handler, same database
// description, helper file untouched, and maintenance over.
func TestReload_FailedBuildKeepsTheOldSnapshot(t *testing.T) {
	cfg, helper := snapshotConfig(t)
	cfg.Reload.Cmd = "false" // fails : introspection is skipped and the build runs against the old schema
	reloader, err := Start(context.Background(), cfg, testLogger())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	oldDb, oldHandler := reloader.CurrentDbInfos(), reloader.Wrapper.inner.Load()
	if err := os.Remove(helper); err != nil {
		t.Fatal(err)
	}

	oldDb.Pool.Close() // the build's privilege query now fails

	reloader.Reload(context.Background())

	if reloader.Wrapper.inner.Load() != oldHandler {
		t.Error("the handler was swapped despite the failed build")
	}
	if reloader.CurrentDbInfos() != oldDb {
		t.Error("the database description was replaced despite the failed build")
	}
	if _, err := os.Stat(helper); !os.IsNotExist(err) {
		t.Errorf("the helper file must be left untouched by a failed build, stat: %v", err)
	}
	reloader.Wrapper.mu.Lock()
	inMaintenance := reloader.Wrapper.maintenance
	reloader.Wrapper.mu.Unlock()
	if inMaintenance {
		t.Error("still in maintenance mode after a failed reload")
	}
}

// reload.cmd failing skips introspection only : the snapshot is still rebuilt (against the old
// schema), swapped in, and the helper file rewritten.
func TestReload_MigrationFailureStillRebuilds(t *testing.T) {
	cfg, helper := snapshotConfig(t)
	cfg.Reload.Cmd = "false"
	reloader, err := Start(context.Background(), cfg, testLogger())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer reloader.CurrentDbInfos().Pool.Close()
	oldHandler := reloader.Wrapper.inner.Load()
	if err := os.Remove(helper); err != nil {
		t.Fatal(err)
	}

	reloader.Reload(context.Background())

	if reloader.Wrapper.inner.Load() == oldHandler {
		t.Error("expected a new handler")
	}
	if _, err := os.Stat(helper); err != nil {
		t.Errorf("expected the helper file to be rewritten after the swap: %v", err)
	}
	if rec := get(reloader.Wrapper, "/hello"); rec.Code != http.StatusOK {
		t.Errorf("expected the new snapshot to serve, got %d", rec.Code)
	}
}
