package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/query"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

var testDb *pg.DbInfos
var testCfg *config.Config
var testHandler http.Handler

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithOrderedInitScripts("../pg/testdata/schema.sql", "testdata/roles.sql"),
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
	testCfg.Pg.Query.AnonymousRole = "~anonymous"
	testHandler = NewRelHandler(testDb, testCfg)

	m.Run()
}

// postRel POSTs body to the handler directly (httptest, no real listener)
// and returns the recorded response.
func postRel(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return postRelTo(t, testHandler, body)
}

func postRelTo(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/rel", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeJSON[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	return v
}

func TestRelHandler_ReadArray(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('HTTP Read Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rec := postRel(t, `{
		"relation": "director", "schema": "public",
		"select": ["own"],
		"where": ["=", "name", ["HTTP Read Director"]]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
	if len(rows) != 1 || rows[0]["name"] != "HTTP Read Director" {
		t.Fatalf("expected 1 row named HTTP Read Director, got %v", rows)
	}
}

func TestRelHandler_ReadArray_EmptyResult(t *testing.T) {
	// streamRows peeks the first row before writing "[" (server/response.go)
	// — a genuinely empty result takes a different, untested-until-now
	// write path ("[]" as one call) than "at least one row" does. Must
	// produce the same "[]" a reader would get either way.
	rec := postRel(t, `{
		"relation": "director", "schema": "public",
		"select": ["own"],
		"where": ["=", "name", ["No Such Director Ever"]]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "[]" {
		t.Fatalf("expected \"[]\", got %q", rec.Body.String())
	}
}

func TestRelHandler_ReadScalarFunction(t *testing.T) {
	rec := postRel(t, `{"function": "fn_plain_add", "schema": "public", "arguments": [2, 3]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	got := decodeJSON[int](t, rec.Body.Bytes())
	if got != 5 {
		t.Fatalf("expected bare scalar 5, got %v (body: %s)", got, rec.Body.String())
	}
}

func TestRelHandler_WriteThenReread(t *testing.T) {
	rec := postRel(t, `{
		"query": {
			"relation": "director", "schema": "public",
			"select": {"id": "id", "name": "name", "movies": "movies"},
			"write_mode": "insert",
			"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": ["own"]}}
		},
		"data": [{"name": "HTTP Write Director", "movies": [{"title": "HTTP Movie"}]}]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), rec.Body.String())
	}
	if rows[0]["id"] == nil {
		t.Fatalf("expected a real, server-generated id in the reread response, got %v", rows[0])
	}
	if rows[0]["name"] != "HTTP Write Director" {
		t.Errorf("expected name=HTTP Write Director, got %v", rows[0]["name"])
	}
	movies, ok := rows[0]["movies"].([]any)
	if !ok || len(movies) != 1 {
		t.Fatalf("expected 1 embedded movie in the reread, got %v", rows[0]["movies"])
	}
}

func TestRelHandler_Sequence_WriteThenReadWhatItWrote(t *testing.T) {
	rec := postRel(t, `[
		{
			"query": {"relation": "director", "schema": "public", "select": ["own"], "write_mode": "insert"},
			"data": [{"name": "Sequence Director"}]
		},
		{
			"relation": "director", "schema": "public", "select": ["own"],
			"where": ["=", "name", ["Sequence Director"]]
		}
	]`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	results := decodeJSON[[]json.RawMessage](t, rec.Body.Bytes())
	if len(results) != 2 {
		t.Fatalf("expected 2 items in the response array, got %d : %s", len(results), rec.Body.String())
	}
	writeRows := decodeJSON[[]map[string]any](t, results[0])
	readRows := decodeJSON[[]map[string]any](t, results[1])
	if len(writeRows) != 1 || len(readRows) != 1 {
		t.Fatalf("expected 1 row from each item, got write=%v read=%v", writeRows, readRows)
	}
	// The plain read (second item) must see the first item's write — proves
	// the shared-commit-before-any-streaming ordering, not just that both
	// work in isolation.
	if readRows[0]["name"] != "Sequence Director" {
		t.Errorf("expected the read to see the just-written row, got %v", readRows[0])
	}
}

func TestRelHandler_TwoWriteItemsInOneSequence(t *testing.T) {
	// Both write items share one "_data" table within the same request —
	// without a shared WriteState, each ExecuteWriteState call would
	// restart __row_id/__node_id at 0 and the second item's COPY would hit
	// a primary-key collision against the first's still-present rows.
	rec := postRel(t, `[
		{
			"query": {"relation": "director", "schema": "public", "select": ["own"], "write_mode": "insert"},
			"data": [{"name": "Sequence Write A"}]
		},
		{
			"query": {"relation": "director", "schema": "public", "select": ["own"], "write_mode": "insert"},
			"data": [{"name": "Sequence Write B"}]
		}
	]`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	results := decodeJSON[[]json.RawMessage](t, rec.Body.Bytes())
	if len(results) != 2 {
		t.Fatalf("expected 2 items, got %d : %s", len(results), rec.Body.String())
	}
	rowsA := decodeJSON[[]map[string]any](t, results[0])
	rowsB := decodeJSON[[]map[string]any](t, results[1])
	if len(rowsA) != 1 || rowsA[0]["name"] != "Sequence Write A" {
		t.Errorf("expected item 0 = Sequence Write A, got %v", rowsA)
	}
	if len(rowsB) != 1 || rowsB[0]["name"] != "Sequence Write B" {
		t.Errorf("expected item 1 = Sequence Write B, got %v", rowsB)
	}
	if rowsA[0]["id"] == rowsB[0]["id"] {
		t.Errorf("expected distinct ids for the two written rows, got %v and %v", rowsA[0]["id"], rowsB[0]["id"])
	}
}

func TestRelHandler_DataDoesNotLeakAcrossRequestsOnReusedConnection(t *testing.T) {
	// Force the pool down to one connection, then plant a stale "_data" row
	// directly (bypassing the handler entirely, simulating an earlier
	// request whose own release-time truncate never ran — a killed process,
	// a swallowed error) before issuing a real request through the handler.
	// A back-to-back pair of clean handler requests can't catch this : the
	// first request's own deferred truncate always runs before the second
	// one starts, so the release-time truncate alone is enough to pass a
	// same-process pair regardless of whether the acquire-time truncate
	// exists — the failure mode this guards against is specifically a
	// cleanup that DIDN'T run.
	container, err := postgres.Run(context.Background(), "postgres:16-alpine",
		postgres.WithOrderedInitScripts("../pg/testdata/schema.sql", "testdata/roles.sql"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	// pool_max_conns=1, not Pool.Config().MaxConns = 1 after the fact :
	// Config() returns a COPY of the pool's config, mutating it post
	// construction has no effect on the already-running pool.
	uri, err := container.ConnectionString(context.Background(), "sslmode=disable", "pool_max_conns=1")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	db, err := pg.NewInfos(uri)
	if err != nil {
		t.Fatalf("NewInfos: %v", err)
	}
	cfg := config.Test()
	cfg.Pg.Query.AnonymousRole = "~anonymous"
	handler := NewRelHandler(db, cfg)

	ctx := context.Background()
	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := conn.Exec(ctx, query.DataTableDDL); err != nil {
		t.Fatalf("create _data: %v", err)
	}
	if _, err := conn.Exec(ctx, `insert into _data (__row_id, __node_id, __parent_id, data, keys)
		values (999, 0, null, '{"name":"Stale Leaked Row"}'::jsonb, '{"id":999999}'::jsonb)`); err != nil {
		t.Fatalf("plant stale row: %v", err)
	}
	conn.Release() // back to the pool ; MaxConns=1 guarantees the handler reacquires this exact connection

	rec := postRelTo(t, handler, `{
		"query": {"relation": "director", "schema": "public", "select": ["own"], "write_mode": "insert"},
		"data": [{"name": "Leak Check B"}]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
	if len(rows) != 1 {
		t.Fatalf("reread leaked the planted stale row : expected 1 row, got %d : %v", len(rows), rows)
	}
	if rows[0]["name"] != "Leak Check B" {
		t.Errorf("expected only this request's own row, got %v", rows)
	}
}

func TestRelHandler_UnwritableSelect_Rejected(t *testing.T) {
	// specs/querying.md ## Configuration : a write whose select omits the
	// identity column must be rejected outright (400, naming the offending
	// relation), not silently accepted and return an empty/wrong reread —
	// see query.findUnwritableNode's own doc comment for the failure mode
	// this guards against (a phantom sequence-generated id matching zero
	// real rows).
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('Unwritable HTTP Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rec := postRel(t, `{
		"query": {"relation": "director", "schema": "public", "select": {"name": "name"}, "write_mode": "update"},
		"data": [{"name": "Renamed"}]
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}

	var name string
	if err := testDb.Pool.QueryRow(ctx, `select name from director where id = $1`, directorID).Scan(&name); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if name != "Unwritable HTTP Director" {
		t.Errorf("expected the row untouched, got name=%q", name)
	}
}

func TestRelHandler_ParseError(t *testing.T) {
	rec := postRel(t, `{not valid json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
	resp := decodeJSON[map[string]any](t, rec.Body.Bytes())
	if resp["status"] != "error" {
		t.Errorf(`expected status="error", got %v`, resp["status"])
	}
}

func TestRelHandler_UnresolvableRelation_BadRequest(t *testing.T) {
	rec := postRel(t, `{"relation": "no_such_relation_at_all", "schema": "public"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
}

func TestRelHandler_WellKnown_NotYetSupported(t *testing.T) {
	rec := postRel(t, `{"wellknown": "some_query"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
}
