// Package tsclient runs the TypeScript client's own tests (typescript/tests) against a real rel server. Go owns
// the lifecycle : a Postgres container loaded with test/hotel/schema.sql and fixture.sql, the real handler tree
// (boot.BuildMux) on a local port, and the database.ts that server generates for it. Then it typechecks the
// tests against that generated file (tsc) and runs them (bun test) with REL_URL pointing at the server.
//
// Skipped when bun isn't installed, so `go test ./...` still works without it.
package tsclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/rel-server/rel/boot"
	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/route"
	"github.com/rel-server/rel/wellknown"
)

func TestTypeScriptClient(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed")
	}

	tsDir, err := filepath.Abs("../../typescript")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tsDir, "node_modules")); err != nil {
		t.Skip("typescript/node_modules is missing : run `bun install` in typescript/")
	}

	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithOrderedInitScripts("../hotel/schema.sql", "../hotel/fixture.sql", "codec_samples.sql"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("starting postgres: %v", err)
	}
	defer func() { _ = container.Terminate(ctx) }()

	uri, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Test()
	cfg.Http.TypeScript.Enable = true
	cfg.Http.TypeScript.Schemas = "hotel"
	cfg.Pg.Query.AnonymousRole = "~anonymous"

	db, err := pg.NewInfosAdminQuery(uri, uri, 0, cfg.Pg.Query.AnonymousRole)
	if err != nil {
		t.Fatalf("introspection: %v", err)
	}
	reg, err := route.BuildRegistry(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	wkReg, err := wellknown.BuildRegistry(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux, err := boot.BuildMux(db, cfg, reg, wkReg, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// The client library as a user gets it : fetched from the running server.
	res, err := http.Get(srv.URL + "/rel/database.ts")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /rel/database.ts: status %d", res.StatusCode)
	}
	generated := filepath.Join(tsDir, "tests", "generated")
	if err := os.MkdirAll(generated, 0o755); err != nil {
		t.Fatal(err)
	}
	database, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generated, "database.ts"), database, 0o644); err != nil {
		t.Fatal(err)
	}
	// Each preset (typescript/presets/<name>) is a program and a test process of its own : its PgWire
	// augmentation is program-wide, and its codec is assigned on the Querier prototype of the process.
	presets, err := filepath.Glob(filepath.Join(tsDir, "presets", "*", "tsconfig.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tsconfig := range presets {
		if err := os.WriteFile(filepath.Join(filepath.Dir(tsconfig), "database.ts"), database, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = tsDir
		cmd.Env = append(os.Environ(), "REL_URL="+srv.URL)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v failed: %v\n%s", name, args, err, output)
		}
		t.Logf("%s %v:\n%s", name, args, output) // shown with -v
	}
	// The tests are typechecked against the generated types too : that is half of what they exist to prove.
	run(bun, "x", "tsc", "--noEmit", "-p", "tests/tsconfig.json")
	// PgWire module augmentation is program-wide, so its check is a program of its own.
	run(bun, "x", "tsc", "--noEmit", "-p", "tests/augment/tsconfig.json")
	run(bun, "test", "./tests")
	for _, tsconfig := range presets {
		preset := filepath.Base(filepath.Dir(tsconfig))
		run(bun, "x", "tsc", "--noEmit", "-p", "presets/"+preset+"/tsconfig.json")
		run(bun, "test", "./presets/"+preset)
	}
}
