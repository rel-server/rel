// Well-known queries through /rel :
// docs/content/query-language/well-known-queries.md ## Calling one over
// POST /rel — invoked exactly like a Relation (bare, a read ; wrapped in
// WriteQuery.query, a write), no separate endpoint. Reuses testDb/testCfg
// from rel_test.go's TestMain (same shared container/schema), but each
// test builds its own *wellknown.Registry (and thus its own handler),
// since the registry is fixed per-request-config and different tests need
// different definition files.
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rel-server/rel/wellknown"
)

// newWellKnownRelHandler writes files to a temp dir, builds a
// *wellknown.Registry over it, and returns a /rel handler wired to it.
func newWellKnownRelHandler(t *testing.T, files map[string]string) http.Handler {
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
	return NewRelHandler(testDb, &cfg, reg)
}

func getRelTo(t *testing.T, handler http.Handler, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/rel?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestRelHandler_WellKnown_POST_Write : a well-known query wrapped in
// WriteQuery.query, written to exactly like a Relation would be.
// TestRelHandler_WellKnown_Sequence : a well-known read composed alongside
// a plain Relation read, same Sequence, same transaction.
// TestRelHandler_WellKnown_Sequence_Write : well-known-queries.md
// ## Behaviour's node-ID-offset caveat — a well-known write sharing a Sequence/WriteState with a plain write.
func TestRelHandler_WellKnown_GET_Read(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('WellKnown GET Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	handler := newWellKnownRelHandler(t, map[string]string{
		"directors.json": `{
			"name": "directors_by_name_get",
			"params": {"name": {"type": "text"}},
			"query": {
				"relation": "director", "schema": "public", "select": ["*~"],
				"where": ["=", ["col", "name"], ["$param", "name", "text"]]
			}
		}`,
	})

	rec := getRelTo(t, handler, "wellknown=directors_by_name_get&params.name=WellKnown+GET+Director")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
	if len(rows) != 1 || rows[0]["name"] != "WellKnown GET Director" {
		t.Fatalf("expected 1 row named WellKnown GET Director, got %v", rows)
	}
}

// TestRelHandler_WellKnown_GET_QuotedParamStaysString : coerceQueryStringLeaves'
// %22-quoted escape hatch for a text-typed param that looks numeric.
func TestRelHandler_WellKnown_GET_QuotedParamStaysString(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('12345')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	handler := newWellKnownRelHandler(t, map[string]string{
		"q.json": `{
			"name": "quoted_param",
			"params": {"name": {"type": "text"}},
			"query": {
				"relation": "director", "schema": "public", "select": ["*~"],
				"where": ["=", ["col", "name"], ["$param", "name", "text"]]
			}
		}`,
	})

	rec := getRelTo(t, handler, `wellknown=quoted_param&params.name=%2212345%22`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
	if len(rows) != 1 || rows[0]["name"] != "12345" {
		t.Fatalf("expected 1 row named 12345, got %v", rows)
	}
}

// TestDecodeGETQuery_WellKnownArrayParamValue : a whole-value JSON array
// URL-encoded into one params.<name>= key round-trips intact.
func TestDecodeGETQuery_WellKnownArrayParamValue(t *testing.T) {
	got, err := decodeGETQuery("wellknown=q&params.tags=%5B1%2C2%2C3%5D")
	if err != nil {
		t.Fatalf("decodeGETQuery: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	params, ok := decoded["params"].(map[string]any)
	if !ok {
		t.Fatalf("expected a params object, got %#v", decoded["params"])
	}
	tags, ok := params["tags"].([]any)
	if !ok || len(tags) != 3 {
		t.Fatalf("expected tags=[1,2,3], got %#v", params["tags"])
	}
}

func TestRelHandler_WellKnown_GET_RejectsData(t *testing.T) {
	handler := newWellKnownRelHandler(t, map[string]string{
		"q.json": `{"name": "no_get_write", "query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}}`,
	})
	rec := getRelTo(t, handler, "wellknown=no_get_write&data.name=Nope")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (GET is read-only), got %d : %s", rec.Code, rec.Body.String())
	}
}
