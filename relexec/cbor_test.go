package relexec

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/rel-server/rel/cborenc"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/query"
	"github.com/rel-server/rel/wellknown"
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

// runCBOR runs v, encoded as CBOR with its dates and times as tag 1, as the request body, in the session time zone
// zone ("" : the server's), with the well-known queries of reg.
func runCBOR(t *testing.T, reg *wellknown.Registry, zone string, v any) (string, error) {
	t.Helper()
	em, err := cbor.EncOptions{Time: cbor.TimeUnixDynamic, TimeTag: cbor.EncTagRequired}.EncMode()
	if err != nil {
		t.Fatal(err)
	}
	b, err := em.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	n, err := query.NewCBORNode(b)
	if err != nil {
		t.Fatalf("NewCBORNode: %v", err)
	}
	pq, err := query.ParseQueryNode(n)
	if err != nil {
		return "", err
	}
	return runParsed(t, testDb, reg, pq, Options{}, zone)
}

func cborDate(days int64) cbor.Tag { return cbor.Tag{Number: 100, Content: days} }

var (
	testUUID      = []byte{0xa0, 0xee, 0xbc, 0x99, 0x9c, 0x0b, 0x4e, 0xf8, 0xbb, 0x6d, 0x6b, 0xb9, 0xbd, 0x38, 0x0a, 0x11}
	testUUIDText  = "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"
	testInstant   = time.UnixMilli(1700000000500) // 2023-11-14 22:13:20.5 UTC
	testDayNumber = int64(20744)                  // 2026-10-18
)

func TestCBOR_TypedValuesInExpressions(t *testing.T) {
	// A filter holding a CBOR date and time, date, UUID or byte string finds the row holding that value.
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into tz_t (id, at, plain, day) values
		(9301, '2023-11-14 22:13:20.5+00', '2023-11-14 22:13:20.5', '2026-10-18'),
		(9302, '294276-12-31 23:59:59+00', null, '0044-03-15 BC'),
		(9303, null, '2023-11-14 23:13:20.5', null) on conflict do nothing`); err != nil {
		t.Fatal(err)
	}
	if _, err := testDb.Pool.Exec(ctx, `insert into enc_t (id, bytes) values (9301, '\x00ff10') on conflict do nothing`); err != nil {
		t.Fatal(err)
	}
	var lateSec, bcDays int64
	if err := testDb.Pool.QueryRow(ctx, `select extract(epoch from at)::int8, (select day - '1970-01-01'::date from tz_t where id = 9302) from tz_t where id = 9302`).Scan(&lateSec, &bcDays); err != nil {
		t.Fatal(err)
	}
	where := func(relation string, cond any) map[string]any {
		return map[string]any{"relation": relation, "schema": "public", "select": map[string]any{"id": []any{"col", "id"}}, "where": []any{"and", []any{">=", []any{"col", "id"}, 9301}, cond}}
	}
	for _, c := range []struct {
		name, zone string
		q          map[string]any
		want       string
	}{
		{"timestamptz", "", where("tz_t", []any{"=", []any{"col", "at"}, testInstant}), `[{"id":9301}]`},
		{"timestamp, in a UTC session", "UTC", where("tz_t", []any{"=", []any{"col", "plain"}, testInstant}), `[{"id":9301}]`},
		{"timestamp, in a Paris session", "Europe/Paris", where("tz_t", []any{"=", []any{"col", "plain"}, testInstant}), `[{"id":9303}]`},
		{"timestamptz near the end of its range", "", where("tz_t", []any{"=", []any{"col", "at"}, time.Unix(lateSec, 0)}), `[{"id":9302}]`},
		{"date", "", where("tz_t", []any{"=", []any{"col", "day"}, cborDate(testDayNumber)}), `[{"id":9301}]`},
		{"date BC", "", where("tz_t", []any{"=", []any{"col", "day"}, cborDate(bcDays)}), `[{"id":9302}]`},
		{"uuid", "", where("tz_t", []any{"and", []any{"=", []any{"col", "id"}, 9301}, []any{"=", []any{"::", cbor.Tag{Number: 37, Content: testUUID}, "text"}, testUUIDText}}), `[{"id":9301}]`},
		{"bytea", "", where("enc_t", []any{"=", []any{"col", "bytes"}, []byte{0x00, 0xff, 0x10}}), `[{"id":9301}]`},
	} {
		out, err := runCBOR(t, nil, c.zone, c.q)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
		} else if out != c.want {
			t.Errorf("%s: got %s, want %s", c.name, out, c.want)
		}
	}

	// a date out of date's range is refused, as it is in a write
	if _, err := runCBOR(t, nil, "", where("tz_t", []any{"=", []any{"col", "day"}, cborDate(1 << 41)})); err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Errorf("an out of range date: err = %v", err)
	}
}

func TestCBOR_WellKnownTypedParams(t *testing.T) {
	// A well-known param takes the CBOR value of its declared type ; a date and time for a timestamp is read in
	// the session zone, as a write's data is.
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into tz_t (id, at, plain, day) values
		(9401, '2023-11-14 22:13:20.5+00', '2023-11-14 22:13:20.5', '2026-10-18'),
		(9402, '2023-11-14 22:13:20.5+00', '2023-11-14 23:13:20.5', '2026-10-18') on conflict do nothing`); err != nil {
		t.Fatal(err)
	}
	reg := newRegistry(t, map[string]string{"typed.json": `{
		"name": "typed_params",
		"params": {"at": {"type": "timestamptz"}, "plain": {"type": "timestamp"}, "day": {"type": "date"}, "tok": {"type": "uuid"}, "bin": {"type": "bytea"}},
		"query": {
			"relation": "tz_t", "schema": "public",
			"select": {"id": ["col", "id"], "tok": ["::", ["$param", "tok", "uuid"], "text"], "bin": ["::", ["$param", "bin", "bytea"], "text"]},
			"where": ["and", [">=", ["col", "id"], 9401],
				["=", ["col", "at"], ["$param", "at", "timestamptz"]],
				["=", ["col", "plain"], ["$param", "plain", "timestamp"]],
				["=", ["col", "day"], ["$param", "day", "date"]]]
		}
	}`})
	params := map[string]any{"at": testInstant, "plain": testInstant, "day": cborDate(testDayNumber), "tok": cbor.Tag{Number: 37, Content: testUUID}, "bin": []byte{0x00, 0xff, 0x10}}
	for zone, id := range map[string]int{"UTC": 9401, "Europe/Paris": 9402} {
		out, err := runCBOR(t, reg, zone, map[string]any{"wellknown": "typed_params", "params": params})
		if err != nil {
			t.Errorf("%s: %v", zone, err)
			continue
		}
		if want := fmt.Sprintf(`[{"bin":"\\x00ff10","id":%d,"tok":%q}]`, id, testUUIDText); out != want {
			t.Errorf("%s: got %s, want %s", zone, out, want)
		}
	}

	// Postgres reads the text through the cast at the use site, whatever the declared type : a date and time
	// declared as a timestamptz, or not declared, is that zone's wall clock under a timestamp cast too
	reg = newRegistry(t, map[string]string{"cast.json": `{
		"name": "cast_only",
		"params": {"declared": {"type": "timestamptz"}, "untyped": {}},
		"query": {
			"relation": "tz_t", "schema": "public", "select": {"id": ["col", "id"]},
			"where": ["and", [">=", ["col", "id"], 9401],
				["=", ["col", "plain"], ["$param", "declared", "timestamp"]],
				["=", ["col", "plain"], ["$param", "untyped", "timestamp"]]]
		}
	}`})
	for zone, id := range map[string]int{"UTC": 9401, "Europe/Paris": 9402} {
		out, err := runCBOR(t, reg, zone, map[string]any{"wellknown": "cast_only", "params": map[string]any{"declared": testInstant, "untyped": testInstant}})
		if err != nil {
			t.Errorf("cast only, %s: %v", zone, err)
		} else if want := fmt.Sprintf(`[{"id":%d}]`, id); out != want {
			t.Errorf("cast only, %s: got %s, want %s", zone, out, want)
		}
	}

	// a CBOR date and time for a numeric param is a type mismatch, as a JSON string is
	reg = newRegistry(t, map[string]string{"n.json": `{
		"name": "numeric_param", "params": {"n": {"type": "int"}},
		"query": {"relation": "tz_t", "schema": "public", "select": {"id": ["col", "id"]}, "where": ["=", ["col", "id"], ["$param", "n", "int"]]}
	}`})
	_, err := runCBOR(t, reg, "", map[string]any{"wellknown": "numeric_param", "params": map[string]any{"n": testInstant}})
	assertClientFault(t, err, errcode.WellKnownParamTypeMismatch)
}
