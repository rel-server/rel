package wellknown

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/pg"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

var testDb *pg.DbInfos
var testCfg *config.Config

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithInitScripts("../pg/testdata/schema.sql"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		panic(err)
	}
	defer func() {
		_ = container.Terminate(ctx)
	}()

	uri, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic(err)
	}

	testDb, err = pg.NewInfos(uri)
	if err != nil {
		panic(err)
	}
	testCfg = config.Test()

	os.Exit(m.Run())
}

// buildRegistry writes files under a fresh temp directory and returns the
// *Registry built against it — throwaway fixtures, never the real filesystem.
func buildRegistry(t *testing.T, files map[string]string) *Registry {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	cfg := *testCfg
	cfg.Pg.Query.WellKnownDirs = dir
	reg, err := BuildRegistry(testDb, &cfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	return reg
}

func TestBuildRegistry_LoadsValidQuery(t *testing.T) {
	reg := buildRegistry(t, map[string]string{
		"directors.json": `{
			"name": "all_directors",
			"query": {"relation": "director", "schema": "public", "select": ["own"]}
		}`,
	})
	c, ok := reg.Lookup("all_directors")
	if !ok {
		t.Fatalf("expected all_directors to be registered")
	}
	if c.Root == nil || c.Read == nil {
		t.Fatalf("expected a resolved root and compiled read statement")
	}
}

func TestBuildRegistry_LoadsYAML(t *testing.T) {
	reg := buildRegistry(t, map[string]string{
		"directors.yaml": "name: yaml_directors\nquery:\n  relation: director\n  schema: public\n  select: [own]\n",
	})
	if _, ok := reg.Lookup("yaml_directors"); !ok {
		t.Fatalf("expected yaml_directors to be registered")
	}
}

func TestBuildRegistry_LoadsHUML(t *testing.T) {
	reg := buildRegistry(t, map[string]string{
		"directors.huml": "name: \"huml_directors\"\nquery::\n  relation: \"director\"\n  schema: \"public\"\n  select:: \"own\"\n",
	})
	if _, ok := reg.Lookup("huml_directors"); !ok {
		t.Fatalf("expected huml_directors to be registered")
	}
}

func TestBuildRegistry_SkipsUnderscorePrefixedFiles(t *testing.T) {
	reg := buildRegistry(t, map[string]string{
		"_ignored.json": `{"name": "ignored", "query": {"relation": "director", "schema": "public", "select": ["own"]}}`,
	})
	if _, ok := reg.Lookup("ignored"); ok {
		t.Fatalf("expected an underscore-prefixed file to be skipped entirely")
	}
}

func TestBuildRegistry_InvalidQueryDeactivatesOnlyThatEntry(t *testing.T) {
	reg := buildRegistry(t, map[string]string{
		"mixed.json": `[
			{"name": "good", "query": {"relation": "director", "schema": "public", "select": ["own"]}},
			{"name": "bad", "query": {"relation": "no_such_relation", "schema": "public", "select": ["own"]}}
		]`,
	})
	if _, ok := reg.Lookup("good"); !ok {
		t.Fatalf("expected the valid sibling entry to still register")
	}
	if _, ok := reg.Lookup("bad"); ok {
		t.Fatalf("expected the invalid entry to be deactivated, not registered")
	}
}

// TestBuildRegistry_DuplicateNameDeactivatesBothEntries :
// docs/content/query-language/well-known-queries.md ## Defining one
// deactivates every entry under a duplicate name, not just the newest.
func TestBuildRegistry_DuplicateNameDeactivatesBothEntries(t *testing.T) {
	reg := buildRegistry(t, map[string]string{
		"a.json": `{"name": "dup", "query": {"relation": "director", "schema": "public", "select": ["own"]}}`,
		"b.json": `{"name": "dup", "query": {"relation": "director", "schema": "public", "select": ["own"]}}`,
	})
	if _, ok := reg.Lookup("dup"); ok {
		t.Fatalf("expected a name collision to deactivate BOTH entries, not register either")
	}
}

func TestBuildRegistry_UnknownParamReferenceDeactivates(t *testing.T) {
	reg := buildRegistry(t, map[string]string{
		"q.json": `{
			"name": "unknown_param",
			"query": {
				"relation": "director", "schema": "public", "select": ["own"],
				"where": ["=", "name", ["$param", "not_declared"]]
			}
		}`,
	})
	if _, ok := reg.Lookup("unknown_param"); ok {
		t.Fatalf("expected a query referencing an undeclared param to be deactivated")
	}
}

func TestBuildRegistry_UnusedDeclaredParamDeactivates(t *testing.T) {
	reg := buildRegistry(t, map[string]string{
		"q.json": `{
			"name": "unused_param",
			"params": {"never_used": {"type": "text"}},
			"query": {"relation": "director", "schema": "public", "select": ["own"]}
		}`,
	})
	if _, ok := reg.Lookup("unused_param"); ok {
		t.Fatalf("expected a declared-but-unused param to deactivate the query")
	}
}

func TestBuildRegistry_MissingDirectorySilentlySkipped(t *testing.T) {
	cfg := *testCfg
	cfg.Pg.Query.WellKnownDirs = "/does/not/exist"
	reg, err := BuildRegistry(testDb, &cfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if len(reg.byName) != 0 {
		t.Fatalf("expected an empty registry, got %d entries", len(reg.byName))
	}
}
