// Well-known queries through Prepare and Execute : invoked exactly like a Relation (bare, a read ;
// wrapped in WriteQuery.query, a write). Each test builds its own registry, since different tests
// need different definition files.
package relexec

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/wellknown"
)

// newRegistry writes files to a temp dir and builds a *wellknown.Registry over it.
func newRegistry(t *testing.T, files map[string]string) *wellknown.Registry {
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
	reg, err := wellknown.BuildRegistry(testDb, &cfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	return reg
}

func TestWellKnown_POST_Read(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('WellKnown Read Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	reg := newRegistry(t, map[string]string{
		"directors.json": `{
			"name": "directors_by_name",
			"params": {"name": {"type": "text"}},
			"query": {
				"relation": "director", "schema": "public", "select": ["*~"],
				"where": ["=", ["col", "name"], ["$param", "name", "text"]]
			}
		}`,
	})

	out, err := runWith(t, testDb, reg, `{"wellknown": "directors_by_name", "params": {"name": "WellKnown Read Director"}}`, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rows := decodeJSON[[]map[string]any](t, []byte(out))
	if len(rows) != 1 || rows[0]["name"] != "WellKnown Read Director" {
		t.Fatalf("expected 1 row named WellKnown Read Director, got %v", rows)
	}
}

func TestWellKnown_POST_UnknownName(t *testing.T) {
	reg := newRegistry(t, nil)
	_, err := runWith(t, testDb, reg, `{"wellknown": "does_not_exist"}`, Options{})
	assertClientFault(t, err, errcode.Code("WELL_KNOWN_UNKNOWN_QUERY"))
}

func TestWellKnown_POST_MissingRequiredParam(t *testing.T) {
	reg := newRegistry(t, map[string]string{
		"q.json": `{
			"name": "needs_param",
			"params": {"name": {"type": "text"}},
			"query": {
				"relation": "director", "schema": "public", "select": ["*~"],
				"where": ["=", ["col", "name"], ["$param", "name", "text"]]
			}
		}`,
	})
	_, err := runWith(t, testDb, reg, `{"wellknown": "needs_param"}`, Options{})
	assertClientFault(t, err, errcode.Code("WELL_KNOWN_PARAM_REQUIRED"))
}

func TestWellKnown_POST_Write(t *testing.T) {
	reg := newRegistry(t, map[string]string{
		"q.json": `{
			"name": "insert_director",
			"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}
		}`,
	})
	out, err := runWith(t, testDb, reg, `{"query": {"wellknown": "insert_director"}, "data": [{"name": "WellKnown Written Director"}]}`, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rows := decodeJSON[[]map[string]any](t, []byte(out))
	if len(rows) != 1 || rows[0]["name"] != "WellKnown Written Director" {
		t.Fatalf("expected 1 written row, got %v", rows)
	}

	var count int
	if err := testDb.Pool.QueryRow(context.Background(), `select count(*) from director where name = 'WellKnown Written Director'`).Scan(&count); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row actually persisted, got %d", count)
	}
}

func TestWellKnown_Sequence(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('WellKnown Sequence Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	reg := newRegistry(t, map[string]string{
		"directors.json": `{
			"name": "all_directors",
			"query": {"relation": "director", "schema": "public", "select": ["*~"], "where": ["=", ["col", "name"], "WellKnown Sequence Director"]}
		}`,
	})

	out, err := runWith(t, testDb, reg, `[
		{"wellknown": "all_directors"},
		{"relation": "director", "schema": "public", "select": ["*~"], "where": ["=", ["col", "name"], "WellKnown Sequence Director"]}
	]`, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	both := decodeJSON[[][]map[string]any](t, []byte(out))
	if len(both) != 2 {
		t.Fatalf("expected 2 items in the sequence response, got %d", len(both))
	}
	if len(both[0]) != 1 || both[0][0]["name"] != "WellKnown Sequence Director" {
		t.Fatalf("expected item 0 (well-known) to return WellKnown Sequence Director, got %v", both[0])
	}
	if len(both[1]) != 1 || both[1][0]["name"] != "WellKnown Sequence Director" {
		t.Fatalf("expected item 1 (plain relation) to return WellKnown Sequence Director, got %v", both[1])
	}
}

func TestWellKnown_Sequence_Write(t *testing.T) {
	reg := newRegistry(t, map[string]string{
		"q.json": `{
			"name": "insert_director_in_sequence",
			"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}
		}`,
	})

	out, err := runWith(t, testDb, reg, `[
		{"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}, "data": [{"name": "WellKnown Sequence Plain Director"}]},
		{"query": {"wellknown": "insert_director_in_sequence"}, "data": [{"name": "WellKnown Sequence WK Director"}]}
	]`, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	both := decodeJSON[[][]map[string]any](t, []byte(out))
	if len(both) != 2 {
		t.Fatalf("expected 2 items in the sequence response, got %d", len(both))
	}
	if len(both[0]) != 1 || both[0][0]["name"] != "WellKnown Sequence Plain Director" {
		t.Fatalf("expected item 0 (plain write) to return WellKnown Sequence Plain Director, got %v", both[0])
	}
	if len(both[1]) != 1 || both[1][0]["name"] != "WellKnown Sequence WK Director" {
		t.Fatalf("expected item 1 (well-known write) to return WellKnown Sequence WK Director, got %v", both[1])
	}

	var count int
	if err := testDb.Pool.QueryRow(context.Background(), `select count(*) from director where name in ('WellKnown Sequence Plain Director', 'WellKnown Sequence WK Director')`).Scan(&count); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 rows actually persisted, got %d", count)
	}
}
