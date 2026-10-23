// Reads, writes, deletes and updates through Prepare and Execute against a real Postgres.
package relexec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/rel-server/rel/pg"
)

func TestQuery_ReadArray(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('HTTP Read Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	out, err := run(t, `{
		"relation": "director", "schema": "public",
		"select": ["*~"],
		"where": ["=", ["col", "name"], "HTTP Read Director"]
	}`, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rows := decodeJSON[[]map[string]any](t, []byte(out))
	if len(rows) != 1 || rows[0]["name"] != "HTTP Read Director" {
		t.Fatalf("expected 1 row named HTTP Read Director, got %v", rows)
	}
}

func TestQuery_Int8AndNumericAreBareJSONNumbersWithEveryDigit(t *testing.T) {
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
			out, err := run(t, `{"relation": "precision_check", "schema": "public", "select": `+sel+`, "where": ["=", ["col", "id"], 1]}`, Options{})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// the raw text, since a Go float64 would round it exactly as a JS client does
			body := out
			for _, want := range []string{`"big":9007199254740993`, `"amount":12345678901234567890.123456789`, `"id":1`} {
				if !strings.Contains(body, want) {
					t.Errorf("expected %s in the response, got %s", want, body)
				}
			}
		})
	}
}

func TestQuery_Int8WhereClauseComparesNumerically(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into precision_check (id, big, amount) values (2, 100, 5)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testDb.Pool.Exec(ctx, `delete from precision_check where id = 2`)
	})

	out, err := run(t, `{
		"relation": "precision_check", "schema": "public",
		"select": ["*~"],
		"where": ["and", ["=", ["col", "id"], 2], [">", ["col", "big"], 50]]
	}`, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rows := decodeJSON[[]map[string]any](t, []byte(out))
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (100 > 50 numerically) — a WHERE clause accidentally comparing as text would behave differently for some values ; got %v", rows)
	}
}

func TestQuery_ReadArray_EmptyResult(t *testing.T) {
	// streamRows takes a different write path for zero rows ("[]" as one
	// call) than for one-or-more — both must produce the same "[]".
	out, err := run(t, `{
		"relation": "director", "schema": "public",
		"select": ["*~"],
		"where": ["=", ["col", "name"], "No Such Director Ever"]
	}`, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "[]" {
		t.Fatalf("expected \"[]\", got %q", out)
	}
}

func TestQuery_ReadScalarFunction(t *testing.T) {
	out, err := run(t, `{"function": "fn_plain_add", "schema": "public", "arguments": [2, 3]}`, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := decodeJSON[int](t, []byte(out))
	if got != 5 {
		t.Fatalf("expected bare scalar 5, got %v (body: %s)", got, out)
	}
}

func TestQuery_WriteThenReread(t *testing.T) {
	out, err := run(t, `{
		"query": {
			"relation": "director", "schema": "public",
			"select": {"id": ["col", "id"], "name": ["col", "name"], "movies": [".", "movies"]},
			"write_mode": "insert",
			"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": ["*~"]}}
		},
		"data": [{"name": "HTTP Write Director", "movies": [{"title": "HTTP Movie"}]}]
	}`, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rows := decodeJSON[[]map[string]any](t, []byte(out))
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), out)
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

func TestQuery_Sequence_WriteThenReadWhatItWrote(t *testing.T) {
	out, err := run(t, `[
		{
			"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"},
			"data": [{"name": "Sequence Director"}]
		},
		{
			"relation": "director", "schema": "public", "select": ["*~"],
			"where": ["=", ["col", "name"], "Sequence Director"]
		}
	]`, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	results := decodeJSON[[]json.RawMessage](t, []byte(out))
	if len(results) != 2 {
		t.Fatalf("expected 2 items in the response array, got %d : %s", len(results), out)
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

func TestQuery_ReadFailureAfterWriteRollsBackTheWrite(t *testing.T) {
	const name = "Rollback Regression Director"
	// This test's pass/fail signal is "does this name exist afterward" ;
	// a leftover row from a previous run must not be there already.
	if _, err := testDb.Pool.Exec(context.Background(), `delete from director where name = $1`, name); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	out, err := run(t, `[
		{
			"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"},
			"data": [{"name": "`+name+`"}]
		},
		{
			"relation": "director", "schema": "public",
			"select": {"boom": ["/", 1, 0]}
		}
	]`, Options{Buffer: 64 << 10})
	// The first item's rows were still held back when the second item failed, so the failure is
	// answered with an error instead of a cut-short body.
	if err == nil {
		t.Fatalf("expected an error, got : %s", out)
	}
	if e := asError(t, err); e.Started {
		t.Fatalf("expected nothing written, the failure came after %d held bytes", len(out))
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

func TestQuery_TwoWriteItemsInOneSequence(t *testing.T) {
	// Without a shared WriteState, both items' __row_id numbering would
	// restart at 0 and the second item's COPY would collide on the PK.
	out, err := run(t, `[
		{
			"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"},
			"data": [{"name": "Sequence Write A"}]
		},
		{
			"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"},
			"data": [{"name": "Sequence Write B"}]
		}
	]`, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	results := decodeJSON[[]json.RawMessage](t, []byte(out))
	if len(results) != 2 {
		t.Fatalf("expected 2 items, got %d : %s", len(results), out)
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

func TestQuery_DataDoesNotLeakAcrossRequestsOnReusedConnection(t *testing.T) {
	// Pool forced to one connection, so every request reuses the same write temp tables ;
	// their rows must be gone after a write, whether the request committed or not.
	container, err := postgres.Run(context.Background(), "postgres:16-alpine",
		postgres.WithOrderedInitScripts("../pg/testdata/schema.sql", "testdata/fixture.sql"),
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
	db, err := pg.NewInfos(uri)
	if err != nil {
		t.Fatalf("NewInfos: %v", err)
	}
	t.Cleanup(db.Pool.Close)
	opts := Options{Policy: Policy{Rollback: true, Stats: true}}
	ctx := context.Background()

	// the rows left in every write temp table of the pool's only connection
	staleRows := func() int {
		conn, err := db.Pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		defer conn.Release()
		names, err := conn.Query(ctx, `select relname from pg_class where relnamespace = pg_my_temp_schema() and relname like '\_\_rel\_w\_%'`)
		if err != nil {
			t.Fatalf("list the write tables: %v", err)
		}
		var tables []string
		for names.Next() {
			var name string
			if err := names.Scan(&name); err != nil {
				t.Fatal(err)
			}
			tables = append(tables, name)
		}
		names.Close()
		total := 0
		for _, name := range tables {
			var n int
			if err := conn.QueryRow(ctx, `select count(*) from pg_temp."`+name+`"`).Scan(&n); err != nil {
				t.Fatalf("count %s: %v", name, err)
			}
			total += n
		}
		return total
	}

	// in order : the first request creates the write temp tables on the pool's only connection
	for _, tc := range []struct{ name, body string }{
		{"committed write", `{"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}, "data": [{"name": "Leak Check A"}]}`},
		{"rolled-back write", `{"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}, "data": [{"name": "Leak Check B"}], "rollback": true, "stats": true}`},
		{"rejected write", `{"query": {"relation": "director", "schema": "public", "select": {"name": ["col", "name"]}, "write_mode": "update-existing"}, "data": [{"name": "Leak Check C"}]}`},
	} {
		_, _ = runOn(t, db, tc.body, opts)
		if n := staleRows(); n != 0 {
			t.Fatalf("%s left %d rows in the write temp tables", tc.name, n)
		}
	}

	// and the next request's own read-back sees only its own row
	out, err := runOn(t, db, `{"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}, "data": [{"name": "Leak Check D"}]}`, opts)
	if err != nil {
		t.Fatal(err)
	}
	rows := decodeJSON[[]map[string]any](t, []byte(out))
	if len(rows) != 1 || rows[0]["name"] != "Leak Check D" {
		t.Errorf("expected only this request's own row, got %v", rows)
	}
}

func TestQuery_WriteOutgoingChildFK(t *testing.T) {
	out, err := run(t, `{
		"query": {
			"relation": "movie", "schema": "public",
			"select": {"id": ["col", "id"], "title": ["col", "title"], "director": [".", "director"]},
			"write_mode": "insert",
			"join": {"director": {"relation": "director", "schema": "public", "on": {"id": "director_id"}, "select": ["*~"]}}
		},
		"data": [{"title": "HTTP Outgoing Movie", "director": {"name": "HTTP Outgoing Director"}}]
	}`, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rows := decodeJSON[[]map[string]any](t, []byte(out))
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

func TestQuery_WhereWordFormOperatorSynonyms(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Word Form Operator Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	wordForm, err := run(t, `{
		"relation": "director", "schema": "public",
		"select": ["*~"],
		"where": ["and", ["eq", ["col", "name"], "Word Form Operator Director"], ["gte", ["col", "id"], 1]]
	}`, Options{})
	if err != nil {
		t.Fatalf("word-form: %v", err)
	}
	symbolForm, err := run(t, `{
		"relation": "director", "schema": "public",
		"select": ["*~"],
		"where": ["and", ["=", ["col", "name"], "Word Form Operator Director"], [">=", ["col", "id"], 1]]
	}`, Options{})
	if err != nil {
		t.Fatalf("symbol-form: %v", err)
	}

	wordRows := decodeJSON[[]map[string]any](t, []byte(wordForm))
	symbolRows := decodeJSON[[]map[string]any](t, []byte(symbolForm))
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

func TestQuery_UnwritableSelect_Rejected(t *testing.T) {
	// ## Configuration : a write whose select omits the identity column
	// must be rejected outright (400), not return an empty/wrong reread.
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('Unwritable HTTP Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	_, err := run(t, `{
		"query": {"relation": "director", "schema": "public", "select": {"name": ["col", "name"]}, "write_mode": "update-existing"},
		"data": [{"name": "Renamed"}]
	}`, Options{})
	assertClientError(t, err)

	var name string
	if err := testDb.Pool.QueryRow(ctx, `select name from director where id = $1`, directorID).Scan(&name); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if name != "Unwritable HTTP Director" {
		t.Errorf("expected the row untouched, got name=%q", name)
	}
}

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

func TestQuery_DeleteMode_RemovesOnlyWhatWhereSelects(t *testing.T) {
	ids := insertDirectors(t, "Delete Me A", "Delete Me B", "Keep Me")
	opts := Options{Policy: Policy{Stats: true}}
	out, err := run(t, fmt.Sprintf(`{
		"query": {"relation": "director", "schema": "public", "write_mode": "delete",
			"where": ["in", ["col", "id"], %d, %d]},
		"stats": true
	}`, ids[0], ids[1]), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp := decodeJSON[map[string]any](t, []byte(out))
	stats, _ := resp["stats"].([]any)
	if len(stats) != 1 || stats[0].(map[string]any)["deleted"] != float64(2) || stats[0].(map[string]any)["table"] != "public.director" {
		t.Errorf("expected stats to report 2 deleted rows, got %v", resp["stats"])
	}
	if got := directorNames(t, ids...); len(got) != 1 || got[ids[2]] != "Keep Me" {
		t.Errorf("expected only the unselected director to remain, got %v", got)
	}

	// without stats the response is the empty selection : a delete returns nothing
	out, err = run(t, fmt.Sprintf(`{"query": {"relation": "director", "schema": "public", "write_mode": "delete", "where": ["=", ["col", "id"], %d]}}`, ids[2]), Options{})
	if err != nil || out != "[]" {
		t.Fatalf("expected [], got %q (err %v)", out, err)
	}
}

func TestQuery_DeleteMode_Rules(t *testing.T) {
	ids := insertDirectors(t, "Delete Rules Director")
	cases := map[string]string{
		"no where": `{"query": {"relation": "director", "schema": "public", "write_mode": "delete"}}`,
		"data":     fmt.Sprintf(`{"query": {"relation": "director", "schema": "public", "write_mode": "delete", "where": ["=", ["col", "id"], %d]}, "data": [{"id": %d}]}`, ids[0], ids[0]),
		"join":     fmt.Sprintf(`{"query": {"relation": "director", "schema": "public", "write_mode": "delete", "where": ["=", ["col", "id"], %d], "join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}}}}}`, ids[0]),
		"nested":   fmt.Sprintf(`{"query": {"relation": "director", "schema": "public", "where": ["=", ["col", "id"], %d], "join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "write_mode": "delete", "where": ["=", ["col", "id"], 1]}}}, "data": []}`, ids[0]),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := run(t, body, Options{})
			assertClientError(t, err)
			if got := directorNames(t, ids...); len(got) != 1 {
				t.Fatalf("a rejected delete must remove nothing, got %v", got)
			}
		})
	}
}

func TestQuery_RootMergeModes_Rejected(t *testing.T) {
	for _, mode := range []string{"deleteonly", "merge", "merge-new", "merge-update"} {
		t.Run(mode, func(t *testing.T) {
			_, err := run(t, fmt.Sprintf(`{"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": %q}, "data": []}`, mode), Options{})
			assertClientError(t, err)
		})
	}
}

func TestQuery_UpdateMode_PatchesEveryMatchedRow(t *testing.T) {
	ids := insertDirectors(t, "Patch A", "Patch B", "Patch Untouched")
	out, err := run(t, fmt.Sprintf(`{
		"query": {"relation": "director", "schema": "public", "write_mode": "update",
			"select": {"id": ["col", "id"], "name": ["col", "name"]},
			"where": ["in", ["col", "id"], %d, %d]},
		"data": {"name": "Patched"}
	}`, ids[0], ids[1]), Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rows := decodeJSON[[]map[string]any](t, []byte(out))
	if len(rows) != 2 || rows[0]["name"] != "Patched" || rows[1]["name"] != "Patched" {
		t.Fatalf("expected both matched rows read back as Patched, got %s", out)
	}
	got := directorNames(t, ids...)
	if got[ids[0]] != "Patched" || got[ids[1]] != "Patched" || got[ids[2]] != "Patch Untouched" {
		t.Errorf("expected only the selected rows patched, got %v", got)
	}
}

func TestQuery_UpdateMode_LeavesUnnamedColumnsAlone(t *testing.T) {
	var studioID, directorID int
	ctx := context.Background()
	if err := testDb.Pool.QueryRow(ctx, `insert into studio (name) values ('Patch Studio') returning id`).Scan(&studioID); err != nil {
		t.Fatalf("insert studio: %v", err)
	}
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name, studio_id) values ('Patch Partial', $1) returning id`, studioID).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	_, err := run(t, fmt.Sprintf(`{
		"query": {"relation": "director", "schema": "public", "write_mode": "update", "select": ["*~"], "where": ["=", ["col", "id"], %d]},
		"data": {"name": "Patch Partial Renamed"}
	}`, directorID), Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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

func TestQuery_UpdateMode_ResponseFollowsTheWhereAfterTheWrite(t *testing.T) {
	ids := insertDirectors(t, "Gone From Where")
	out, err := run(t, fmt.Sprintf(`{
		"query": {"relation": "director", "schema": "public", "write_mode": "update", "select": ["*~"],
			"where": ["=", ["col", "name"], "Gone From Where"]},
		"data": {"name": "No Longer Matches"}
	}`), Options{})
	if err != nil || out != "[]" {
		t.Fatalf("expected [] (the where is read again after the write), got %q (err %v)", out, err)
	}
	if got := directorNames(t, ids...); got[ids[0]] != "No Longer Matches" {
		t.Errorf("expected the row updated anyway, got %v", got)
	}
}

func TestQuery_UpdateMode_Rules(t *testing.T) {
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
			_, err := run(t, body, Options{})
			assertClientError(t, err)
			if got := directorNames(t, ids...); got[ids[0]] != "Update Rules Director" {
				t.Fatalf("a rejected update must change nothing, got %v", got)
			}
		})
	}
}

func TestQuery_UpdateMode_CanChangeAPrimaryKey(t *testing.T) {
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
			out, err := run(t, fmt.Sprintf(`{
				"query": {"relation": %q, "schema": "public", "write_mode": "update", "select": ["*~"], "where": ["=", ["col", "code"], "patch-pk"]},
				"data": {"id": %d}
			}`, tc.table, newID), Options{})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected Postgres to refuse changing a generated-always identity column, got 200 : %s", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			rows := decodeJSON[[]map[string]any](t, []byte(out))
			if len(rows) != 1 || rows[0]["id"] != fmt.Sprint(newID) && rows[0]["id"] != float64(newID) {
				t.Errorf("expected the row read back under its new id %d, got %s", newID, out)
			}
		})
	}
}
