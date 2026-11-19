package server

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/pg"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// CBOR columns need postcbor, which only this image has.
const postcborImage = "ghcr.io/ceymard/postgres-convenient:18.7-alpine"

const cborColumnSchema = `
create extension postcbor;
create schema blob;
create domain blob.cbor as bytea;
create domain public.not_cbor as bytea;
create table doc (
	id int primary key,
	body cbor,
	raw blob.cbor,
	raws blob.cbor[],
	plain not_cbor,
	tag text
);
create function first_raw() returns blob.cbor language sql as $$ select raw from doc order by id limit 1 $$;
create role "~anonymous";
grant usage on schema blob to "~anonymous";
grant all on doc to "~anonymous";
grant execute on function first_raw() to "~anonymous";
`

func cborColumnHandler(t *testing.T) (http.Handler, *pg.DbInfos) {
	t.Helper()
	ctx := context.Background()
	container, err := postgres.Run(ctx, postcborImage, postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("starting postgres container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	uri, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	setup, err := pg.NewInfos(uri)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Pool.Exec(ctx, cborColumnSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	setup.Pool.Close()

	cfg := config.Test()
	cfg.Pg.Query.AnonymousRole = "~anonymous"
	db, err := pg.NewInfosAdminQuery(uri, uri, 0, cfg.Pg.Query.AnonymousRole)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Pool.Close)
	return NewRelHandler(db, cfg, nil), db
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sendRel(h http.Handler, body []byte, contentType, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/rel", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRelHandler_CBORColumns(t *testing.T) {
	h, db := cborColumnHandler(t)
	ctx := context.Background()
	if !db.HasCBOR {
		t.Fatal("no CBOR column type recognised")
	}

	// [structure {a: 1}, reference {a: 2}] : records the client used, which must not be stored
	withStructures := mustHex(t, "82"+"d9dfff8319e000816161"+"01"+"d9e0008102")
	spliced := mustHex(t, "82a1616101a1616102")
	intKeys := mustHex(t, "a2016178f7f820") // {1: "x", undefined: simple(32)} : no JSON form at all

	write := func(rows ...map[string]any) *httptest.ResponseRecorder {
		data := make([]any, len(rows))
		for i, r := range rows {
			data[i] = r
		}
		return sendRel(h, cborBody(t, map[string]any{
			"query": map[string]any{"relation": "doc", "schema": "public", "select": []any{"*~"}, "write_mode": "insert"},
			"data":  data,
		}), "application/cbor", "application/cbor")
	}
	rec := write(
		map[string]any{"id": 1, "raw": cbor.RawMessage(withStructures), "body": cbor.RawMessage(withStructures),
			"raws": []any{cbor.RawMessage(intKeys), nil}, "plain": []byte{1, 2}, "tag": "one"},
		map[string]any{"id": 2, "raw": cbor.RawMessage(intKeys), "tag": "two"},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("CBOR write : status %d : %s", rec.Code, rec.Body.String())
	}

	t.Run("a bytea domain stores the item spliced, an item without structures as written", func(t *testing.T) {
		for id, want := range map[int][]byte{1: spliced, 2: intKeys} {
			var got []byte
			if err := db.Pool.QueryRow(ctx, `select raw from doc where id = $1`, id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("row %d : raw is % x, want % x", id, got, want)
			}
		}
		var elem []byte
		if err := db.Pool.QueryRow(ctx, `select raws[1] from doc where id = 1`).Scan(&elem); err != nil || !bytes.Equal(elem, intKeys) {
			t.Errorf("raws[1] is % x (%v), want % x", elem, err, intKeys)
		}
	})

	t.Run("postcbor receives the item without structures", func(t *testing.T) {
		var got string
		if err := db.Pool.QueryRow(ctx, `select body::jsonb::text from doc where id = 1`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != `[{"a": 1}, {"a": 2}]` {
			t.Errorf("body is %s", got)
		}
	})

	t.Run("a CBOR response carries the stored values as they are", func(t *testing.T) {
		rec := sendRel(h, cborBody(t, map[string]any{"relation": "doc", "schema": "public", "select": []any{"*~"},
			"where": []any{"=", []any{".", "id"}, 1}}), "application/cbor", "application/cbor")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d : %s", rec.Code, rec.Body.String())
		}
		var rows []map[string]cbor.RawMessage
		if err := cbor.Unmarshal(rec.Body.Bytes(), &rows); err != nil || len(rows) != 1 {
			t.Fatalf("response % x : %v", rec.Body.Bytes(), err)
		}
		row := rows[0]
		if !bytes.Equal(row["raw"], spliced) {
			t.Errorf("raw is % x, want % x", []byte(row["raw"]), spliced)
		}
		var body []map[string]int
		if err := cbor.Unmarshal(row["body"], &body); err != nil || len(body) != 2 || body[0]["a"] != 1 || body[1]["a"] != 2 {
			t.Errorf("body is % x (%v)", []byte(row["body"]), err)
		}
		if want := append(mustHex(t, "82"), append(intKeys, 0xf6)...); !bytes.Equal(row["raws"], want) {
			t.Errorf("raws is % x, want % x", []byte(row["raws"]), want)
		}
		if !bytes.Equal(row["plain"], mustHex(t, "420102")) {
			t.Errorf("a bytea domain not named cbor is a byte string : % x", []byte(row["plain"]))
		}
	})

	t.Run("a bytea-domain value at the root of a response is its CBOR, not a byte string", func(t *testing.T) {
		for name, q := range map[string]map[string]any{
			"scalar select": {"relation": "doc", "schema": "public", "select": []any{".", "raw"}, "where": []any{"=", []any{".", "id"}, 2}},
			"function":      {"function": "first_raw", "schema": "public"},
		} {
			rec := sendRel(h, cborBody(t, q), "application/cbor", "application/cbor")
			if rec.Code != http.StatusOK {
				t.Fatalf("%s : status %d : %s", name, rec.Code, rec.Body.String())
			}
			want := append(mustHex(t, "9f"), append(intKeys, 0xff)...)
			if name == "function" {
				want = spliced
			}
			if !bytes.Equal(rec.Body.Bytes(), want) {
				t.Errorf("%s : response % x, want % x", name, rec.Body.Bytes(), want)
			}
		}
	})

	t.Run("a JSON response refuses what holds a CBOR value, and takes the rest", func(t *testing.T) {
		for _, c := range []struct {
			name, query string
			refused     bool
		}{
			{"the default selection", `{"relation": "doc", "schema": "public"}`, true},
			{"a postcbor column", `{"relation": "doc", "schema": "public", "select": {"b": [".", "body"]}}`, true},
			{"an array of a bytea-domain one", `{"relation": "doc", "schema": "public", "select": ["*~", ["id", "tag", "body", "raw", "plain"]]}`, true},
			{"a coalesce of one", `{"relation": "doc", "schema": "public", "select": {"r": ["coalesce", [".", "raw"], [".", "raw"]]}}`, true},
			{"a scalar function", `{"function": "first_raw", "schema": "public"}`, true},
			{"a star inside an object", `{"relation": "doc", "schema": "public", "select": {"all": ["*~", ["plain"]]}}`, true},
			{"other columns", `{"relation": "doc", "schema": "public", "select": ["*~", ["body", "raw", "raws"]], "where": ["=", [".", "id"], 1]}`, false},
			{"a cast to jsonb", `{"relation": "doc", "schema": "public", "select": {"b": ["::", [".", "body"], "jsonb"]}, "where": ["=", [".", "id"], 1]}`, false},
			{"a filter on one", `{"relation": "doc", "schema": "public", "select": {"t": [".", "tag"]}, "where": ["is_not_null", [".", "raw"]]}`, false},
		} {
			rec := sendRel(h, []byte(c.query), "application/json", "")
			if c.refused {
				if rec.Code != http.StatusBadRequest || errcode.Code(rec.Header().Get(errcode.Header)) != errcode.CBORColumnNeedsCBOR {
					t.Errorf("%s : status %d, %s : %s", c.name, rec.Code, rec.Header().Get(errcode.Header), rec.Body.String())
				}
				continue
			}
			if rec.Code != http.StatusOK {
				t.Errorf("%s : status %d : %s", c.name, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("a JSON write whose selection holds no CBOR column goes through", func(t *testing.T) {
		rec := sendRel(h, []byte(`{"query": {"relation": "doc", "schema": "public", "write_mode": "insert",
			"select": ["*~", ["body", "raw", "raws"]]}, "data": [{"id": 4, "tag": "four"}]}`), "application/json", "")
		if rec.Code != http.StatusOK {
			t.Errorf("status %d : %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("a JSON write whose selection holds a CBOR column is refused, even when the rows leave it out", func(t *testing.T) {
		rec := sendRel(h, []byte(`{"query": {"relation": "doc", "schema": "public", "write_mode": "insert", "select": ["*~"]},
			"data": [{"id": 5, "tag": "five"}], "returns": "none", "stats": true}`), "application/json", "")
		if rec.Code != http.StatusBadRequest || errcode.Code(rec.Header().Get(errcode.Header)) != errcode.CBORColumnNeedsCBOR {
			t.Errorf("status %d, %s : %s", rec.Code, rec.Header().Get(errcode.Header), rec.Body.String())
		}
	})

	t.Run("a JSON request does not write a CBOR column", func(t *testing.T) {
		for _, col := range []string{"raw", "body"} {
			// "set" : written, never read, so only the write is refused
			rec := sendRel(h, []byte(`{"query": {"relation": "doc", "schema": "public", "write_mode": "insert",
				"select": {"id": ["col", "id"], "v": ["set", "`+col+`"]}}, "data": [{"id": 3, "v": {"a": 1}}]}`), "application/json", "")
			if rec.Code != http.StatusBadRequest || errcode.Code(rec.Header().Get(errcode.Header)) != errcode.CBORColumnNeedsCBOR {
				t.Errorf("%s : status %d, %s : %s", col, rec.Code, rec.Header().Get(errcode.Header), rec.Body.String())
			}
		}
	})

	t.Run("a stored value that is not one CBOR item fails the response", func(t *testing.T) {
		if _, err := db.Pool.Exec(ctx, `insert into doc (id, raw) values (10, '\x8201')`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `delete from doc where id = 10`) })
		rec := sendRel(h, cborBody(t, map[string]any{"relation": "doc", "schema": "public", "select": []any{".", "raw"},
			"where": []any{"=", []any{".", "id"}, 10}}), "application/cbor", "application/cbor")
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status %d : % x", rec.Code, rec.Body.Bytes())
		}
	})

	t.Run("an array element that is an array is one CBOR item, not a dimension", func(t *testing.T) {
		rec := write(map[string]any{"id": 20, "raws": []any{[]any{1, 2}, []any{3, 4}}}, map[string]any{"id": 21, "raws": []any{[]any{1}, []any{2, 3}}})
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d : %s", rec.Code, rec.Body.String())
		}
		for id, want := range map[int][]string{20: {"820102", "820304"}, 21: {"8101", "820203"}} {
			var elems [][]byte
			if err := db.Pool.QueryRow(ctx, `select raws::bytea[] from doc where id = $1`, id).Scan(&elems); err != nil {
				t.Fatal(err)
			}
			if len(elems) != len(want) || hex.EncodeToString(elems[0]) != want[0] || hex.EncodeToString(elems[1]) != want[1] {
				t.Errorf("row %d : raws is % x, want %v", id, elems, want)
			}
		}
	})

	t.Run("an expression of a bytea-domain value at the root is its CBOR too", func(t *testing.T) {
		for name, c := range map[string]struct {
			sel  any
			want []byte
		}{
			"coalesce": {[]any{"coalesce", []any{".", "raw"}, []any{".", "raw"}}, spliced},
			"index":    {[]any{"index", []any{".", "raws"}, 1}, intKeys},
		} {
			rec := sendRel(h, cborBody(t, map[string]any{"relation": "doc", "schema": "public", "select": c.sel,
				"where": []any{"=", []any{".", "id"}, 1}}), "application/cbor", "application/cbor")
			if want := append(append([]byte{0x9f}, c.want...), 0xff); rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), want) {
				t.Errorf("%s : status %d, % x, want % x", name, rec.Code, rec.Body.Bytes(), want)
			}
		}
	})
}
