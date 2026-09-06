package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rel-server/rel/config"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestTypescriptOutFlag(t *testing.T) {
	cases := []struct {
		args     []string
		wantOk   bool
		wantPath string
	}{
		{nil, false, ""},
		{[]string{}, false, ""},
		{[]string{"--other=x"}, false, ""},
		{[]string{"--typescript-out=out.ts"}, true, "out.ts"},
		{[]string{"--typescript-out", "out.ts"}, true, "out.ts"},
		{[]string{"--typescript-out", "-"}, true, "-"},
		{[]string{"--typescript-out"}, true, ""}, // trailing flag, no value
		{[]string{"--other=x", "--typescript-out=/tmp/database.ts"}, true, "/tmp/database.ts"},
	}
	for _, c := range cases {
		path, ok := typescriptOutFlag(c.args)
		if ok != c.wantOk || path != c.wantPath {
			t.Errorf("typescriptOutFlag(%v) = (%q, %v), want (%q, %v)", c.args, path, ok, c.wantPath, c.wantOk)
		}
	}
}

// TestRunTypeScriptExport is an end-to-end check that --typescript-out's
// entire body works against a real database, without going anywhere near
// dmut, /route, well-known queries, or an HTTP listener.
func TestRunTypeScriptExport(t *testing.T) {
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithInitScripts("../../pg/testdata/schema.sql"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("starting postgres container: %v", err)
	}
	defer func() { _ = container.Terminate(ctx) }()

	uri, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("getting connection string: %v", err)
	}

	cfg := config.Test()
	cfg.Pg.URI = uri

	dir := t.TempDir()
	outPath := filepath.Join(dir, "database.ts")
	if err := runTypeScriptExport(cfg, outPath); err != nil {
		t.Fatalf("runTypeScriptExport: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading generated file: %v", err)
	}
	if !strings.Contains(string(data), "export interface Relations {") {
		t.Errorf("generated database.ts missing Relations interface ; got:\n%s", data)
	}
	if !strings.Contains(string(data), "export class Querier") {
		t.Errorf("generated database.ts missing Querier (section 1) ; got:\n%s", data)
	}
}
