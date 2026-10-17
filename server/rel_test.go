package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

var testDb *pg.DbInfos
var testCfg *config.Config
var testHandler http.Handler
var testDbURI string

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
	testDbURI = uri

	testCfg = config.Test()
	testCfg.Pg.Query.AnonymousRole = "~anonymous"

	// This package's anonymous-access scenarios need AnonymousRoleExists
	// true, hence the real role name here.
	testDb, err = pg.NewInfosAdminQuery(uri, uri, 0, testCfg.Pg.Query.AnonymousRole)
	if err != nil {
		panic(err)
	}

	testHandler = NewRelHandler(testDb, testCfg, nil)

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
		"select": ["*~"],
		"where": ["=", ["col", "name"], "HTTP Read Director"]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
	if len(rows) != 1 || rows[0]["name"] != "HTTP Read Director" {
		t.Fatalf("expected 1 row named HTTP Read Director, got %v", rows)
	}
}

// docs/content/typescript/index.md ## Typed wire values : int8 and numeric arrive as bare JSON numbers, with
// every digit Postgres has. Rounding to the nearest float64 (9007199254740992, a different value) happens in
// a client's JSON.parse, which is why the client can be given a JSON implementation that keeps big numbers.
func TestRelHandler_Int8AndNumericAreBareJSONNumbersWithEveryDigit(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into precision_check (id, big, amount) values (1, 9007199254740993, 12345678901234567890.123456789)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testDb.Pool.Exec(ctx, `delete from precision_check where id = 1`)
	})

	for name, sel := range map[string]string{
		"the default select": `["*~"]`,
		"an explicit col":    `{"id": ["col", "id"], "big": ["col", "big"], "amount": ["col", "amount"]}`,
		"a get":              `{"id": ["get", "id"], "big": ["get", "big"], "amount": ["get", "amount"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := postRel(t, `{"relation": "precision_check", "schema": "public", "select": `+sel+`, "where": ["=", ["col", "id"], 1]}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
			}
			// the raw text, since a Go float64 would round it exactly as a JS client does
			body := rec.Body.String()
			for _, want := range []string{`"big":9007199254740993`, `"amount":12345678901234567890.123456789`, `"id":1`} {
				if !strings.Contains(body, want) {
					t.Errorf("expected %s in the response, got %s", want, body)
				}
			}
		})
	}
}

// A WHERE clause filtering on an int8 column compares numerically.
func TestRelHandler_Int8WhereClauseComparesNumerically(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into precision_check (id, big, amount) values (2, 100, 5)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testDb.Pool.Exec(ctx, `delete from precision_check where id = 2`)
	})

	rec := postRel(t, `{
		"relation": "precision_check", "schema": "public",
		"select": ["*~"],
		"where": ["and", ["=", ["col", "id"], 2], [">", ["col", "big"], 50]]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (100 > 50 numerically) — a WHERE clause accidentally comparing as text would behave differently for some values ; got %v", rows)
	}
}

func TestRelHandler_ReadArray_EmptyResult(t *testing.T) {
	// streamRows takes a different write path for zero rows ("[]" as one
	// call) than for one-or-more — both must produce the same "[]".
	rec := postRel(t, `{
		"relation": "director", "schema": "public",
		"select": ["*~"],
		"where": ["=", ["col", "name"], "No Such Director Ever"]
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
			"select": {"id": ["col", "id"], "name": ["col", "name"], "movies": [".", "movies"]},
			"write_mode": "insert",
			"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": ["*~"]}}
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
			"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"},
			"data": [{"name": "Sequence Director"}]
		},
		{
			"relation": "director", "schema": "public", "select": ["*~"],
			"where": ["=", ["col", "name"], "Sequence Director"]
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
	// The plain read must see the first item's write — proves the shared
	// commit-before-streaming ordering, not just that both work alone.
	if readRows[0]["name"] != "Sequence Director" {
		t.Errorf("expected the read to see the just-written row, got %v", readRows[0])
	}
}

// TestRelHandler_ReadFailureAfterWriteRollsBackTheWrite : a read failing
// mid-stream must roll back the whole transaction, write included.
func TestRelHandler_ReadFailureAfterWriteRollsBackTheWrite(t *testing.T) {
	const name = "Rollback Regression Director"
	// This test's pass/fail signal is "does this name exist afterward" ;
	// a leftover row from a previous run must not be there already.
	if _, err := testDb.Pool.Exec(context.Background(), `delete from director where name = $1`, name); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	rec := postRel(t, `[
		{
			"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"},
			"data": [{"name": "`+name+`"}]
		},
		{
			"relation": "director", "schema": "public",
			"select": {"boom": ["/", 1, 0]}
		}
	]`)
	// The first item already streamed bytes before the second item's
	// division-by-zero fails — no clean envelope is possible at that point.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the response to already be mid-stream (200 sent on first byte), got %d : %s", rec.Code, rec.Body.String())
	}
	if json.Valid(rec.Body.Bytes()) {
		t.Fatalf("expected a truncated/invalid JSON body (streaming aborted mid-request), got complete JSON: %s", rec.Body.String())
	}

	// The real assertion : the first item's write must not be visible,
	// proving the rollback rather than an already-committed write.
	var count int
	if err := testDb.Pool.QueryRow(context.Background(), `select count(*) from director where name = $1`, name).Scan(&count); err != nil {
		t.Fatalf("querying back: %v", err)
	}
	if count != 0 {
		t.Errorf("expected the write to be rolled back by the later read failure, but found %d row(s) : %s", count, name)
	}
}

func TestRelHandler_TwoWriteItemsInOneSequence(t *testing.T) {
	// Without a shared WriteState, both items' __row_id numbering would
	// restart at 0 and the second item's COPY would collide on the PK.
	rec := postRel(t, `[
		{
			"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"},
			"data": [{"name": "Sequence Write A"}]
		},
		{
			"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"},
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
	// Pool forced to one connection, so every request reuses the same "_data" ;
	// its rows must be gone after a write, whether the request committed or not.
	container, err := postgres.Run(context.Background(), "postgres:16-alpine",
		postgres.WithOrderedInitScripts("../pg/testdata/schema.sql", "testdata/roles.sql"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	// pool_max_conns=1 here, not Pool.Config().MaxConns after the fact :
	// Config() returns a copy, mutating it post-construction is a no-op.
	uri, err := container.ConnectionString(context.Background(), "sslmode=disable", "pool_max_conns=1")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	cfg := config.Test()
	cfg.Pg.Query.AnonymousRole = "~anonymous"
	db, err := pg.NewInfosAdminQuery(uri, uri, 0, cfg.Pg.Query.AnonymousRole)
	if err != nil {
		t.Fatalf("NewInfosAdminQuery: %v", err)
	}
	handler := NewRelHandler(db, cfg, nil)
	ctx := context.Background()

	staleRows := func() int {
		conn, err := db.Pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		defer conn.Release()
		var n int
		if err := conn.QueryRow(ctx, `select count(*) from _data`).Scan(&n); err != nil {
			t.Fatalf("count _data: %v", err)
		}
		return n
	}

	// in order : the first request creates "_data" on the pool's only connection
	for _, tc := range []struct{ name, body string }{
		{"committed write", `{"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}, "data": [{"name": "Leak Check A"}]}`},
		{"rolled-back write", `{"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}, "data": [{"name": "Leak Check B"}], "rollback": true, "stats": true}`},
		{"rejected write", `{"query": {"relation": "director", "schema": "public", "select": {"name": ["col", "name"]}, "write_mode": "update-existing"}, "data": [{"name": "Leak Check C"}]}`},
	} {
		postRelTo(t, handler, tc.body)
		if n := staleRows(); n != 0 {
			t.Fatalf("%s left %d rows in _data", tc.name, n)
		}
	}

	// and the next request's own read-back sees only its own row
	rec := postRelTo(t, handler, `{"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}, "data": [{"name": "Leak Check D"}]}`)
	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
	if len(rows) != 1 || rows[0]["name"] != "Leak Check D" {
		t.Errorf("expected only this request's own row, got %v", rows)
	}
}

func TestRelHandler_WriteOutgoingChildFK_HTTP(t *testing.T) {
	rec := postRel(t, `{
		"query": {
			"relation": "movie", "schema": "public",
			"select": {"id": ["col", "id"], "title": ["col", "title"], "director": [".", "director"]},
			"write_mode": "insert",
			"join": {"director": {"relation": "director", "schema": "public", "on": {"id": "director_id"}, "select": ["*~"]}}
		},
		"data": [{"title": "HTTP Outgoing Movie", "director": {"name": "HTTP Outgoing Director"}}]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}

	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
	if len(rows) != 1 || rows[0]["title"] != "HTTP Outgoing Movie" {
		t.Fatalf("expected 1 row titled 'HTTP Outgoing Movie', got %v", rows)
	}
	director, ok := rows[0]["director"].(map[string]any)
	if !ok || director["name"] != "HTTP Outgoing Director" {
		t.Fatalf("expected the linked director echoed back, got %v", rows[0]["director"])
	}

	ctx := context.Background()
	var count int
	if err := testDb.Pool.QueryRow(ctx, `
		select count(*) from movie m
		join director d on d.id = m.director_id
		where m.title = 'HTTP Outgoing Movie' and d.name = 'HTTP Outgoing Director'
	`).Scan(&count); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected the movie correctly linked to its new director in the database, got count=%d", count)
	}
}

// TestRelHandler_WhereWordFormOperatorSynonyms_HTTP : a POST /rel body
// using word-form operators ("gte" not ">=") must match the symbolic form.
func TestRelHandler_WhereWordFormOperatorSynonyms_HTTP(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Word Form Operator Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	wordForm := postRel(t, `{
		"relation": "director", "schema": "public",
		"select": ["*~"],
		"where": ["and", ["eq", ["col", "name"], "Word Form Operator Director"], ["gte", ["col", "id"], 1]]
	}`)
	if wordForm.Code != http.StatusOK {
		t.Fatalf("word-form: expected 200, got %d : %s", wordForm.Code, wordForm.Body.String())
	}
	symbolForm := postRel(t, `{
		"relation": "director", "schema": "public",
		"select": ["*~"],
		"where": ["and", ["=", ["col", "name"], "Word Form Operator Director"], [">=", ["col", "id"], 1]]
	}`)
	if symbolForm.Code != http.StatusOK {
		t.Fatalf("symbol-form: expected 200, got %d : %s", symbolForm.Code, symbolForm.Body.String())
	}

	wordRows := decodeJSON[[]map[string]any](t, wordForm.Body.Bytes())
	symbolRows := decodeJSON[[]map[string]any](t, symbolForm.Body.Bytes())
	if len(wordRows) != 1 || len(symbolRows) != 1 {
		t.Fatalf("expected exactly 1 row from each spelling, got word=%d symbol=%d", len(wordRows), len(symbolRows))
	}
	if wordRows[0]["name"] != "Word Form Operator Director" {
		t.Errorf("expected the word-form query to find the row, got %v", wordRows[0])
	}
	if wordRows[0]["id"] != symbolRows[0]["id"] {
		t.Errorf("expected both spellings to return the identical row, got word=%v symbol=%v", wordRows[0], symbolRows[0])
	}
}

func TestRelHandler_UnwritableSelect_Rejected(t *testing.T) {
	// ## Configuration : a write whose select omits the identity column
	// must be rejected outright (400), not return an empty/wrong reread.
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('Unwritable HTTP Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rec := postRel(t, `{
		"query": {"relation": "director", "schema": "public", "select": {"name": ["col", "name"]}, "write_mode": "update-existing"},
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

// TestRelHandler_WellKnown_UnregisteredName_BarePosition : testHandler's
// nil *wellknown.Registry makes any bare well-known query "unregistered".
func TestRelHandler_WellKnown_UnregisteredName_BarePosition(t *testing.T) {
	rec := postRel(t, `{"wellknown": "some_query"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Rel-Errorcode"); got != "WELL_KNOWN_UNKNOWN_QUERY" {
		t.Fatalf("expected WELL_KNOWN_UNKNOWN_QUERY, got %q", got)
	}
}

// TestRelHandler_WellKnown_UnregisteredName_WrappedWritePosition is the
// wrapped-write counterpart of the bare-position test above.
func TestRelHandler_WellKnown_UnregisteredName_WrappedWritePosition(t *testing.T) {
	rec := postRel(t, `{"query": {"wellknown": "some_query"}, "data": [{"a": 1}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Rel-Errorcode"); got != "WELL_KNOWN_UNKNOWN_QUERY" {
		t.Fatalf("expected WELL_KNOWN_UNKNOWN_QUERY, got %q", got)
	}
}

// insertDirectors adds one director per name and returns their ids.
func insertDirectors(t *testing.T, names ...string) []int {
	t.Helper()
	ids := make([]int, len(names))
	for i, n := range names {
		if err := testDb.Pool.QueryRow(context.Background(), `insert into director (name) values ($1) returning id`, n).Scan(&ids[i]); err != nil {
			t.Fatalf("insert director: %v", err)
		}
	}
	return ids
}

func directorNames(t *testing.T, ids ...int) map[int]string {
	t.Helper()
	out := map[int]string{}
	rows, err := testDb.Pool.Query(context.Background(), `select id, name from director where id = any($1)`, ids)
	if err != nil {
		t.Fatalf("select directors: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[id] = name
	}
	return out
}

func TestRelHandler_DeleteMode_RemovesOnlyWhatWhereSelects(t *testing.T) {
	ids := insertDirectors(t, "Delete Me A", "Delete Me B", "Keep Me")
	handler := complexHandler(t, func(c *config.Config) { c.Pg.Query.AllowStats = true })
	rec := postRelTo(t, handler, fmt.Sprintf(`{
		"query": {"relation": "director", "schema": "public", "write_mode": "delete",
			"where": ["in", ["col", "id"], %d, %d]},
		"stats": true
	}`, ids[0], ids[1]))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	resp := decodeJSON[map[string]any](t, rec.Body.Bytes())
	stats, _ := resp["stats"].([]any)
	if len(stats) != 1 || stats[0].(map[string]any)["deleted"] != float64(2) || stats[0].(map[string]any)["table"] != "public.director" {
		t.Errorf("expected stats to report 2 deleted rows, got %v", resp["stats"])
	}
	if got := directorNames(t, ids...); len(got) != 1 || got[ids[2]] != "Keep Me" {
		t.Errorf("expected only the unselected director to remain, got %v", got)
	}

	// without stats the response is the empty selection : a delete returns nothing
	rec = postRel(t, fmt.Sprintf(`{"query": {"relation": "director", "schema": "public", "write_mode": "delete", "where": ["=", ["col", "id"], %d]}}`, ids[2]))
	if rec.Code != http.StatusOK || rec.Body.String() != "[]" {
		t.Fatalf("expected 200 and [], got %d : %s", rec.Code, rec.Body.String())
	}
}

func TestRelHandler_DeleteMode_Rules(t *testing.T) {
	ids := insertDirectors(t, "Delete Rules Director")
	cases := map[string]string{
		"no where": `{"query": {"relation": "director", "schema": "public", "write_mode": "delete"}}`,
		"data":     fmt.Sprintf(`{"query": {"relation": "director", "schema": "public", "write_mode": "delete", "where": ["=", ["col", "id"], %d]}, "data": [{"id": %d}]}`, ids[0], ids[0]),
		"join":     fmt.Sprintf(`{"query": {"relation": "director", "schema": "public", "write_mode": "delete", "where": ["=", ["col", "id"], %d], "join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}}}}}`, ids[0]),
		"nested":   fmt.Sprintf(`{"query": {"relation": "director", "schema": "public", "where": ["=", ["col", "id"], %d], "join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "write_mode": "delete", "where": ["=", ["col", "id"], 1]}}}, "data": []}`, ids[0]),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := postRel(t, body); rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
			}
			if got := directorNames(t, ids...); len(got) != 1 {
				t.Fatalf("a rejected delete must remove nothing, got %v", got)
			}
		})
	}
}

func TestRelHandler_RootMergeModes_Rejected(t *testing.T) {
	for _, mode := range []string{"deleteonly", "merge", "merge-new", "merge-update"} {
		t.Run(mode, func(t *testing.T) {
			rec := postRel(t, fmt.Sprintf(`{"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": %q}, "data": []}`, mode))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRelHandler_UpdateMode_PatchesEveryMatchedRow(t *testing.T) {
	ids := insertDirectors(t, "Patch A", "Patch B", "Patch Untouched")
	rec := postRel(t, fmt.Sprintf(`{
		"query": {"relation": "director", "schema": "public", "write_mode": "update",
			"select": {"id": ["col", "id"], "name": ["col", "name"]},
			"where": ["in", ["col", "id"], %d, %d]},
		"data": {"name": "Patched"}
	}`, ids[0], ids[1]))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
	if len(rows) != 2 || rows[0]["name"] != "Patched" || rows[1]["name"] != "Patched" {
		t.Fatalf("expected both matched rows read back as Patched, got %s", rec.Body.String())
	}
	got := directorNames(t, ids...)
	if got[ids[0]] != "Patched" || got[ids[1]] != "Patched" || got[ids[2]] != "Patch Untouched" {
		t.Errorf("expected only the selected rows patched, got %v", got)
	}
}

func TestRelHandler_UpdateMode_LeavesUnnamedColumnsAlone(t *testing.T) {
	var studioID, directorID int
	ctx := context.Background()
	if err := testDb.Pool.QueryRow(ctx, `insert into studio (name) values ('Patch Studio') returning id`).Scan(&studioID); err != nil {
		t.Fatalf("insert studio: %v", err)
	}
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name, studio_id) values ('Patch Partial', $1) returning id`, studioID).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	rec := postRel(t, fmt.Sprintf(`{
		"query": {"relation": "director", "schema": "public", "write_mode": "update", "select": ["*~"], "where": ["=", ["col", "id"], %d]},
		"data": {"name": "Patch Partial Renamed"}
	}`, directorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	var name string
	var gotStudio *int
	if err := testDb.Pool.QueryRow(ctx, `select name, studio_id from director where id = $1`, directorID).Scan(&name, &gotStudio); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if name != "Patch Partial Renamed" || gotStudio == nil || *gotStudio != studioID {
		t.Errorf("expected name patched and studio_id kept, got %q / %v", name, gotStudio)
	}
}

func TestRelHandler_UpdateMode_ResponseFollowsTheWhereAfterTheWrite(t *testing.T) {
	ids := insertDirectors(t, "Gone From Where")
	rec := postRel(t, fmt.Sprintf(`{
		"query": {"relation": "director", "schema": "public", "write_mode": "update", "select": ["*~"],
			"where": ["=", ["col", "name"], "Gone From Where"]},
		"data": {"name": "No Longer Matches"}
	}`))
	if rec.Code != http.StatusOK || rec.Body.String() != "[]" {
		t.Fatalf("expected 200 and [] (the where is read again after the write), got %d : %s", rec.Code, rec.Body.String())
	}
	if got := directorNames(t, ids...); got[ids[0]] != "No Longer Matches" {
		t.Errorf("expected the row updated anyway, got %v", got)
	}
}

func TestRelHandler_UpdateMode_Rules(t *testing.T) {
	ids := insertDirectors(t, "Update Rules Director")
	where := fmt.Sprintf(`["=", ["col", "id"], %d]`, ids[0])
	cases := map[string]string{
		"no where":     `{"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "update"}, "data": {"name": "x"}}`,
		"array data":   `{"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "update", "where": ` + where + `}, "data": [{"name": "x"}]}`,
		"unknown col":  `{"query": {"relation": "director", "schema": "public", "select": {"name": ["col", "name"]}, "write_mode": "update", "where": ` + where + `}, "data": {"studio_id": 1}}`,
		"nested":       `{"query": {"relation": "studio", "schema": "public", "select": ["*~"], "where": ["=", ["col", "id"], 1], "join": {"directors": {"relation": "director", "schema": "public", "on": {"studio_id": "id"}, "write_mode": "update", "where": ` + where + `}}}, "data": []}`,
		"writing join": `{"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "update", "where": ` + where + `, "join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "write_mode": "merge"}}}, "data": {"name": "x"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := postRel(t, body); rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
			}
			if got := directorNames(t, ids...); got[ids[0]] != "Update Rules Director" {
				t.Fatalf("a rejected update must change nothing, got %v", got)
			}
		})
	}
}

func TestRelHandler_UpdateMode_CanChangeAPrimaryKey(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		table   string
		wantErr bool
	}{{"identity_keyed_default", false}, {"identity_keyed_always", true}} {
		t.Run(tc.table, func(t *testing.T) {
			var id int
			if err := testDb.Pool.QueryRow(ctx, `insert into `+tc.table+` (code, name) values ('patch-pk', 'n') returning id`).Scan(&id); err != nil {
				t.Fatalf("insert: %v", err)
			}
			newID := id + 100000
			rec := postRel(t, fmt.Sprintf(`{
				"query": {"relation": %q, "schema": "public", "write_mode": "update", "select": ["*~"], "where": ["=", ["col", "code"], "patch-pk"]},
				"data": {"id": %d}
			}`, tc.table, newID))
			if tc.wantErr {
				if rec.Code == http.StatusOK {
					t.Fatalf("expected Postgres to refuse changing a generated-always identity column, got 200 : %s", rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
			}
			rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
			if len(rows) != 1 || rows[0]["id"] != fmt.Sprint(newID) && rows[0]["id"] != float64(newID) {
				t.Errorf("expected the row read back under its new id %d, got %s", newID, rec.Body.String())
			}
		})
	}
}

// TestRelHandler_UnknownRole_FailsCleanly proves a role the database doesn't know is a server
// error that leaves the pooled connection usable (the batched begin + SET LOCAL ROLE is rolled back).
func TestRelHandler_UnknownRole_FailsCleanly(t *testing.T) {
	cfg := *testCfg
	cfg.Pg.Query.AnonymousRole = "no_such_role_at_all"
	broken := NewRelHandler(testDb, &cfg, nil)
	for range 3 { // more than one request, so a connection left in an aborted transaction would show
		rec := postRelTo(t, broken, `{"relation": "director", "schema": "public", "limit": 1, "select": ["*~"]}`)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d : %s", rec.Code, rec.Body.String())
		}
	}
	if rec := postRel(t, `{"relation": "director", "schema": "public", "limit": 1, "select": ["*~"]}`); rec.Code != http.StatusOK {
		t.Fatalf("expected the pool to stay usable, got %d : %s", rec.Code, rec.Body.String())
	}
}

// TestRelHandler_UnresolvableOverload_Is400 proves Postgres' refusal to pick a function overload for the
// argument types a request produced (sum of text) is the request's own error : a 400 with its own code.
func TestRelHandler_UnresolvableOverload_Is400(t *testing.T) {
	rec := postRel(t, `{"relation": "agg_parent", "schema": "public", "join": {"kids": {"relation": "agg_child", "schema": "public", "on": {"parent_id": "id"}}},
		"select": {"v": ["agg", "sum", [["::", [".", "kids", "amount"], "text"]]]}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
	if code := errCode(t, rec.Body.Bytes()); code != "PG_UNDEFINED_FUNCTION" {
		t.Errorf("expected PG_UNDEFINED_FUNCTION, got %s : %s", code, rec.Body.String())
	}
}
