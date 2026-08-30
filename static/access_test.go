package static

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/pg"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

var testDb *pg.DbInfos

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithInitScripts("testdata/schema.sql"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		panic(err)
	}
	defer func() { _ = container.Terminate(ctx) }()

	uri, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic(err)
	}

	testDb, err = pg.NewInfosAdminQuery(uri, uri, 0, "~anonymous")
	if err != nil {
		panic(err)
	}

	os.Exit(m.Run())
}

func setReject(t *testing.T, reject bool) {
	t.Helper()
	_, err := testDb.Pool.Exec(context.Background(), "update access_control set reject = $1", reject)
	if err != nil {
		t.Fatalf("updating access_control: %v", err)
	}
}

func newGatedServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "private"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "private", "secret.txt"), []byte("gated content"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "public.txt"), []byte("public content"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	srv := New(config.Http{
		Static: config.HttpStatic{
			Path: dir,
			Access: map[string]config.StaticAccessRule{
				"private": {Prefix: "private/", Function: "public.check_static_access"},
			},
		},
	})
	if srv == nil {
		t.Fatalf("expected a non-nil server")
	}
	return srv, dir
}

func serveGated(srv *Server, db *pg.DbInfos, cfg *config.Config, path string) *httptest.ResponseRecorder {
	handler := http.StripPrefix("/static/", srv.Handler(db, cfg))
	req := httptest.NewRequest(http.MethodGet, "/static/"+path, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestAccessControl_ExistenceCheckedFirst_MissingFileIs404NotGateError
// proves the existence check happens BEFORE the DB call, in the ordinary
// case (anonymous access enabled) : a missing file under a gated prefix is
// a plain 404, even when the gate function would otherwise reject with a
// distinctly different status (403) — if the DB call had run first, this
// request would see 403, not 404.
func TestAccessControl_ExistenceCheckedFirst_MissingFileIs404NotGateError(t *testing.T) {
	srv, _ := newGatedServer(t)
	setReject(t, true)
	defer setReject(t, false)

	cfg := &config.Config{}
	rec := serveGated(srv, testDb, cfg, "private/does-not-exist.txt")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 (existence-first, DB never consulted), got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAccessControl_ExistingFile_GateRejects proves the gate function's own
// rejection is honored for a file that DOES exist.
func TestAccessControl_ExistingFile_GateRejects(t *testing.T) {
	srv, _ := newGatedServer(t)
	setReject(t, true)
	defer setReject(t, false)

	cfg := &config.Config{}
	rec := serveGated(srv, testDb, cfg, "private/secret.txt")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 from check_static_access's own rejection, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAccessControl_ExistingFile_GateAllows proves the file is actually
// served once the gate function allows it.
func TestAccessControl_ExistingFile_GateAllows(t *testing.T) {
	srv, _ := newGatedServer(t)
	setReject(t, false)

	cfg := &config.Config{}
	rec := serveGated(srv, testDb, cfg, "private/secret.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "gated content" {
		t.Errorf("unexpected body: %q", rec.Body.String())
	}
}

// TestAccessControl_AnonymousDisabled_401BeforeExistenceCheck proves the
// anonymous-access-disabled case 401s BEFORE the existence check — even a
// nonexistent file under a gated prefix gets 401, not 404.
func TestAccessControl_AnonymousDisabled_401BeforeExistenceCheck(t *testing.T) {
	srv, _ := newGatedServer(t)

	disabledDb := *testDb
	disabledDb.AnonymousRoleExists = false

	cfg := &config.Config{}
	rec := serveGated(srv, &disabledDb, cfg, "private/does-not-exist.txt")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 before the existence check, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAccessControl_UngatedPrefix_UnaffectedByAnonymousDisabled proves an
// ungated prefix is served exactly as before, regardless of the
// anonymous-role-existence state.
func TestAccessControl_UngatedPrefix_UnaffectedByAnonymousDisabled(t *testing.T) {
	srv, _ := newGatedServer(t)

	disabledDb := *testDb
	disabledDb.AnonymousRoleExists = false

	cfg := &config.Config{}
	rec := serveGated(srv, &disabledDb, cfg, "public.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for an ungated path even with anonymous access disabled, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "public content" {
		t.Errorf("unexpected body: %q", rec.Body.String())
	}
}
