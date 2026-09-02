package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ceymard/rel/wellknown"
)

// newWellKnownHandler writes files under a fresh temp directory, points a
// throwaway config copy's WellKnownDirs at it, builds a *wellknown.Registry
// against the SAME testDb/testCfg rel_test.go's TestMain already set up
// (this package's one TestMain — a second isn't allowed), and returns a
// ready-to-use handler.
func newWellKnownHandler(t *testing.T, files map[string]string) http.Handler {
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
	return NewWellKnownHandler(testDb, &cfg, reg)
}

func postWellKnown(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/wellknown", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func getWellKnown(t *testing.T, handler http.Handler, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/wellknown?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestWellKnownHandler_POST_Read(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('WellKnown Read Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	handler := newWellKnownHandler(t, map[string]string{
		"directors.json": `{
			"name": "directors_by_name",
			"params": {"name": {"type": "text"}},
			"query": {
				"relation": "director", "schema": "public", "select": ["own"],
				"where": ["=", "name", ["$param", "name", "text"]]
			}
		}`,
	})

	rec := postWellKnown(t, handler, `{"name": "directors_by_name", "params": {"name": "WellKnown Read Director"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
	if len(rows) != 1 || rows[0]["name"] != "WellKnown Read Director" {
		t.Fatalf("expected 1 row named WellKnown Read Director, got %v", rows)
	}
}

func TestWellKnownHandler_POST_UnknownName(t *testing.T) {
	handler := newWellKnownHandler(t, nil)
	rec := postWellKnown(t, handler, `{"name": "does_not_exist"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Rel-Errorcode"); got != "WELL_KNOWN_UNKNOWN_QUERY" {
		t.Fatalf("expected WELL_KNOWN_UNKNOWN_QUERY, got %q", got)
	}
}

func TestWellKnownHandler_POST_MissingRequiredParam(t *testing.T) {
	handler := newWellKnownHandler(t, map[string]string{
		"q.json": `{
			"name": "needs_param",
			"params": {"name": {"type": "text"}},
			"query": {
				"relation": "director", "schema": "public", "select": ["own"],
				"where": ["=", "name", ["$param", "name", "text"]]
			}
		}`,
	})
	rec := postWellKnown(t, handler, `{"name": "needs_param"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Rel-Errorcode"); got != "WELL_KNOWN_PARAM_REQUIRED" {
		t.Fatalf("expected WELL_KNOWN_PARAM_REQUIRED, got %q", got)
	}
}

func TestWellKnownHandler_POST_Write(t *testing.T) {
	handler := newWellKnownHandler(t, map[string]string{
		"q.json": `{
			"name": "insert_director",
			"query": {"relation": "director", "schema": "public", "select": ["own"], "write_mode": "insert"}
		}`,
	})
	rec := postWellKnown(t, handler, `{"name": "insert_director", "data": [{"name": "WellKnown Written Director"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
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

func TestWellKnownHandler_GET_Read(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('WellKnown GET Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	handler := newWellKnownHandler(t, map[string]string{
		"directors.json": `{
			"name": "directors_by_name_get",
			"params": {"name": {"type": "text"}},
			"query": {
				"relation": "director", "schema": "public", "select": ["own"],
				"where": ["=", "name", ["$param", "name", "text"]]
			}
		}`,
	})

	rec := getWellKnown(t, handler, "name=directors_by_name_get&params.name=WellKnown+GET+Director")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
	if len(rows) != 1 || rows[0]["name"] != "WellKnown GET Director" {
		t.Fatalf("expected 1 row named WellKnown GET Director, got %v", rows)
	}
}

// TestWellKnownHandler_GET_QuotedParamStaysString pins
// coerceQueryStringLeaves' escape hatch : a text-typed param whose value
// looks numeric must still be reachable via GET by URL-encoding explicit
// JSON quotes around it, rather than being permanently rejected by the
// type check (an unquoted "12345" would coerce to the number 12345 and
// fail a text-typed param's check).
func TestWellKnownHandler_GET_QuotedParamStaysString(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('12345')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	handler := newWellKnownHandler(t, map[string]string{
		"q.json": `{
			"name": "quoted_param",
			"params": {"name": {"type": "text"}},
			"query": {
				"relation": "director", "schema": "public", "select": ["own"],
				"where": ["=", "name", ["$param", "name", "text"]]
			}
		}`,
	})

	rec := getWellKnown(t, handler, `name=quoted_param&params.name=%2212345%22`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
	if len(rows) != 1 || rows[0]["name"] != "12345" {
		t.Fatalf("expected 1 row named 12345, got %v", rows)
	}
}

func TestWellKnownHandler_GET_RejectsData(t *testing.T) {
	handler := newWellKnownHandler(t, map[string]string{
		"q.json": `{"name": "no_get_write", "query": {"relation": "director", "schema": "public", "select": ["own"], "write_mode": "insert"}}`,
	})
	rec := getWellKnown(t, handler, "name=no_get_write&data.name=Nope")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (GET is read-only), got %d : %s", rec.Code, rec.Body.String())
	}
}

func TestWellKnownHandler_MethodNotAllowed(t *testing.T) {
	handler := newWellKnownHandler(t, nil)
	req := httptest.NewRequest(http.MethodPut, "/wellknown", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}
