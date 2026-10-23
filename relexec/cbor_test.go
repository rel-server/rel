package relexec

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/rel-server/rel/cborenc"
	"github.com/rel-server/rel/query"
)

func decodeCBOR[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	dm, err := cbor.DecOptions{DefaultMapType: reflect.TypeOf(map[string]any{})}.DecMode()
	if err != nil {
		t.Fatal(err)
	}
	if err := dm.Unmarshal(data, &v); err != nil {
		t.Fatalf("not CBOR (%v) : %x", err, data)
	}
	return v
}

func TestCBOR_ShapesAndTypes(t *testing.T) {
	// nested joins, typed columns and a sequence, answered with the same content as JSON
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into tz_t (id, at, plain, day) values (9201, '2023-11-14 22:13:20.5+00', '2026-10-18 14:30:00', '2026-10-18') on conflict do nothing`); err != nil {
		t.Fatal(err)
	}
	if _, err := testDb.Pool.Exec(ctx, `with d as (insert into director (name) values ('Relexec CBOR Director') returning id) insert into movie (title, director_id) select 'Relexec CBOR Movie', id from d`); err != nil {
		t.Fatal(err)
	}
	body := `[
		{"relation": "tz_t", "schema": "public", "select": ["*~"], "where": ["=", ["col", "id"], 9201]},
		{"relation": "director", "schema": "public", "select": {"name": ["col", "name"], "movies": [".", "movies"]}, "where": ["=", ["col", "name"], "Relexec CBOR Director"],
		 "join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": {"title": ["col", "title"]}}}}
	]`
	j, err := run(t, body, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := run(t, body, Options{Format: cborenc.CBOR})
	if err != nil {
		t.Fatal(err)
	}
	jr := decodeJSON[[][]map[string]any](t, []byte(j))
	cr := decodeCBOR[[][]map[string]any](t, []byte(c))
	if len(cr) != 2 || len(cr[0]) != len(jr[0]) || len(cr[1]) != len(jr[1]) || len(cr[1]) != 1 {
		t.Fatalf("shapes differ : %s / %v", j, cr)
	}
	if cr[0][0]["id"] != uint64(9201) {
		t.Errorf("id = %#v", cr[0][0]["id"])
	}
	at, ok := cr[0][0]["at"].(time.Time)
	if !ok || at.UnixNano() != 1700000000500000000 {
		t.Errorf("at = %#v", cr[0][0]["at"])
	}
	if m := cr[1][0]["movies"].([]any); len(m) != 1 || len(m) != len(jr[1][0]["movies"].([]any)) {
		t.Errorf("movies = %v", m)
	}
}

func TestCBOR_Envelope(t *testing.T) {
	opts := Options{Format: cborenc.CBOR, Policy: Policy{Rollback: true, Stats: true, Count: true, SQL: true, QueryPlan: true}}
	body := `{
		"query": {"relation": "director", "schema": "public", "select": ["*~"]},
		"data": [{"name": "Relexec Accept Rollback Director"}],
		"stats": true, "sql": true, "rollback": true
	}`
	out, err := run(t, body, opts)
	if err != nil {
		t.Fatal(err)
	}
	env := decodeCBOR[map[string]any](t, []byte(out))
	if res, _ := env["result"].([]any); len(res) != 1 || res[0].(map[string]any)["name"] != "Relexec Accept Rollback Director" {
		t.Errorf("result = %v", env["result"])
	}
	for _, k := range []string{"stats", "sql"} {
		if _, ok := env[k].([]any); !ok {
			t.Errorf("%s = %#v", k, env[k])
		}
	}
	if n := countRows(t, `select count(*) from director where name = 'Relexec Accept Rollback Director'`); n != 0 {
		t.Errorf("rolled back write persisted : %d", n)
	}

	// a read's plan is EXPLAIN's JSON, transcoded
	out, err = run(t, `{"query": {"relation": "director", "schema": "public", "select": ["*~"], "limit": 1}, "query_plan": true}`, opts)
	if err != nil {
		t.Fatal(err)
	}
	env = decodeCBOR[map[string]any](t, []byte(out))
	if plans, ok := env["query_plan"].([]any); !ok || len(plans) != 1 {
		t.Errorf("query_plan = %#v", env["query_plan"])
	}

	// a read with count
	out, err = run(t, `{"query": {"relation": "director", "schema": "public", "select": ["*~"], "limit": 1}, "count": true}`, opts)
	if err != nil {
		t.Fatal(err)
	}
	env = decodeCBOR[map[string]any](t, []byte(out))
	if _, ok := env["count"].(uint64); !ok || env["limit"] != uint64(1) {
		t.Errorf("envelope = %v", env)
	}
	// returns none
	out, err = run(t, `{"query": {"relation": "director", "schema": "public", "select": ["*~"]}, "count": true, "returns": "none"}`, opts)
	if err != nil {
		t.Fatal(err)
	}
	if env = decodeCBOR[map[string]any](t, []byte(out)); env["result"] != nil {
		t.Errorf("returns none, got %v", env)
	}
	if _, has := env["result"]; has {
		t.Errorf("returns none, got %v", env)
	}
}

func TestCBOR_WriteFastPathAndWellKnown(t *testing.T) {
	// the single-node write answers from its own binary rows
	out, err := run(t, `{"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}, "data": [{"name": "Relexec Fast A"}, {"name": "Relexec Fast B"}]}`, Options{Format: cborenc.CBOR})
	if err != nil {
		t.Fatal(err)
	}
	if rows := decodeCBOR[[]map[string]any](t, []byte(out)); len(rows) != 2 || rows[0]["name"] != "Relexec Fast A" {
		t.Errorf("rows = %v", rows)
	}

	reg := newRegistry(t, map[string]string{"d.json": `{"name": "by_name", "params": {"name": {"type": "text"}},
		"query": {"relation": "director", "schema": "public", "select": ["*~"], "where": ["=", ["col", "name"], ["$param", "name", "text"]]}}`})
	out, err = runWith(t, testDb, reg, `{"wellknown": "by_name", "params": {"name": "Relexec Fast B"}}`, Options{Format: cborenc.CBOR})
	if err != nil {
		t.Fatal(err)
	}
	if rows := decodeCBOR[[]map[string]any](t, []byte(out)); len(rows) != 1 || rows[0]["name"] != "Relexec Fast B" {
		t.Errorf("well-known rows = %x", out)
	}
}

// A response in the structures format reads as the plain CBOR one and defines each layout once
// across the items of a multi-item request.
func TestCBOR_Structures(t *testing.T) {
	if _, err := testDb.Pool.Exec(context.Background(), `insert into director (name) select 'Relexec Structures Director ' || g from generate_series(1, 5) g`); err != nil {
		t.Fatal(err)
	}
	one := `{"relation": "director", "schema": "public", "select": ["*~"], "where": ["like", ["col", "name"], "Relexec Structures Director%"], "order_by": [["col", "name"]]}`
	other := `{"relation": "movie", "schema": "public", "select": ["*~"], "where": ["=", ["col", "id"], -1]}`
	multi := "[" + one + "," + other + "," + one + "]"

	for name, body := range map[string]string{"one item": one, "three items": multi} {
		plain, err := run(t, body, Options{Format: cborenc.CBOR})
		if err != nil {
			t.Fatal(err)
		}
		structs, err := run(t, body, Options{Format: cborenc.CBORStructures})
		if err != nil {
			t.Fatal(err)
		}
		want, err := query.NewCBORNode([]byte(plain))
		if err != nil {
			t.Fatal(err)
		}
		got, err := query.NewCBORNode([]byte(structs))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		a, _ := want.Raw()
		b, err := got.Raw()
		if err != nil || a != b {
			t.Errorf("%s: plain %s\nstructures %s (%v)", name, a, b, err)
		}
		if n := bytes.Count([]byte(structs), []byte{0xd9, 0xdf, 0xff}); n != 1 {
			// one layout for the directors ; the empty movie result writes none
			t.Errorf("%s: %d definitions, want 1", name, n)
		}
		if len(structs) >= len(plain) {
			t.Errorf("%s: %d bytes with structures, %d without", name, len(structs), len(plain))
		}
	}

	// a write's read-back uses them too
	out, err := run(t, `{"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}, "data": [{"name": "Relexec Structures Fast A"}, {"name": "Relexec Structures Fast B"}]}`, Options{Format: cborenc.CBORStructures})
	if err != nil {
		t.Fatal(err)
	}
	n, err := query.NewCBORNode([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if items, err := n.ArrayItems(); err != nil || len(items) != 2 {
		t.Errorf("write read-back: %d rows, %v", len(items), err)
	}
}
