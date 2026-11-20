package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// A type with no CBOR form of its own (ltree, citext, hstore) is answered in CBOR as the JSON response gives it :
// ltree and citext as their text, hstore as the map its jsonb cast gives, a composite holding one as a map of JSON
// values. At the root, in an incoming and an outgoing join, through expressions rel can type, and in a write's
// read-back.
func TestRelHandler_CBORExtensionTypesAsJSONForm(t *testing.T) {
	both := func(name, body string) any {
		t.Helper()
		j := postAccepting(t, testHandler, body, "")
		c := postAccepting(t, testHandler, body, "application/cbor")
		if j.Code != http.StatusOK || c.Code != http.StatusOK {
			t.Fatalf("%s : status JSON %d, CBOR %d : %s", name, j.Code, c.Code, c.Body.String())
		}
		want, got := decodeJSON[any](t, j.Body.Bytes()), decodeCBOR[any](t, c.Body.Bytes())
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s : CBOR %#v, JSON %#v", name, got, want)
		}
		return got
	}
	check := func(name string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s : %#v, want %#v", name, got, want)
		}
	}

	cols := `"path": ["col", "path"], "paths": ["col", "paths"], "label": ["col", "label"], "attrs": ["col", "attrs"], "pair": ["col", "pair"], "pairs": ["col", "pairs"]`
	full := map[string]any{"path": "a.b", "paths": []any{"a.b", "c"}, "label": "Ext Owner", "attrs": map[string]any{"k": "v"},
		"pair": map[string]any{"path": "p.q", "at": "2026-01-01T00:00:00+00:00", "raw": `\x01`}, "pairs": []any{map[string]any{"path": "x.y", "at": nil, "raw": nil}}}
	empty := map[string]any{"path": "e", "paths": []any{}, "label": "Ext Owner empty", "attrs": nil, "pair": nil, "pairs": nil}
	null := map[string]any{"path": nil, "paths": nil, "label": "Ext Owner null", "attrs": nil, "pair": nil, "pairs": nil}
	items := []any{
		map[string]any{"label": "Ext 1 empty", "paths": []any{}},
		map[string]any{"label": "Ext 2 null", "paths": nil},
		map[string]any{"label": "Ext 3 two", "paths": []any{"x", "y.z"}},
	}

	// the write's read-back, in CBOR only : a JSON twin would write the rows again
	write := `{"query": {"relation": "ext_owner", "schema": "public", "write_mode": "insert",
		"select": {"id": ["col", "id"], ` + cols + `, "items": [".", "items"]},
		"join": {"items": {"relation": "ext_item", "schema": "public", "on": {"owner_id": "id"}, "write_mode": "insert",
			"select": {"id": ["col", "id"], "label": ["col", "label"], "paths": ["col", "paths"]}}}},
		"data": [
			{"path": "a.b", "paths": ["a.b", "c"], "label": "Ext Owner", "attrs": "k=>v",
				"pair": {"path": "p.q", "at": "2026-01-01T00:00:00Z", "raw": "\\x01"}, "pairs": [{"path": "x.y", "at": null, "raw": null}],
				"items": [{"label": "Ext 3 two", "paths": ["x", "y.z"]}, {"label": "Ext 1 empty", "paths": []}, {"label": "Ext 2 null", "paths": null}]},
			{"path": "e", "paths": [], "label": "Ext Owner empty", "items": [{"label": "Ext 4 under empty", "paths": ["q"]}]},
			{"path": null, "paths": null, "label": "Ext Owner null", "items": [{"label": "Ext 5 under null", "paths": null}]}]}`
	rec := postAccepting(t, testHandler, write, "application/cbor")
	if rec.Code != http.StatusOK {
		t.Fatalf("write : status %d : %s", rec.Code, rec.Body.String())
	}
	readBack := decodeCBOR[[]map[string]any](t, rec.Body.Bytes())
	if len(readBack) != 3 {
		t.Fatalf("write read-back : %#v", readBack)
	}
	itemPaths := map[string]any{}
	for _, owner := range readBack {
		list, _ := owner["items"].([]any)
		for _, it := range list {
			m, _ := it.(map[string]any)
			itemPaths[m["label"].(string)] = m["paths"]
		}
		delete(owner, "items")
		delete(owner, "id")
	}
	check("write read-back", readBack, []map[string]any{full, empty, null})
	check("write read-back items", itemPaths, map[string]any{"Ext 1 empty": []any{}, "Ext 2 null": nil, "Ext 3 two": []any{"x", "y.z"}, "Ext 4 under empty": []any{"q"}, "Ext 5 under null": nil})

	owners := `"where": ["like", ["col", "label"], "Ext Owner%"], "order_by": [["col", "label"]]`
	check("root *~", both("root *~", `{"relation": "ext_owner", "schema": "public", "select": ["*~", ["id"]], `+owners+`}`), []any{full, empty, null})
	check("root col", both("root col", `{"relation": "ext_owner", "schema": "public", "select": {`+cols+`}, `+owners+`}`), []any{full, empty, null})
	check("root scalar", both("root scalar", `{"relation": "ext_owner", "schema": "public", "select": ["col", "paths"], `+owners+`}`),
		[]any{[]any{"a.b", "c"}, []any{}, nil})

	withItems := map[string]any{"items": items}
	for k, v := range full {
		withItems[k] = v
	}
	check("incoming join", both("incoming join", `{"relation": "ext_owner", "schema": "public", "select": {`+cols+`, "items": [".", "items"]},
		"where": ["=", ["col", "label"], "ext owner"],
		"join": {"items": {"relation": "ext_item", "schema": "public", "on": {"owner_id": "id"}, "order_by": [["col", "label"]],
			"select": {"label": ["col", "label"], "paths": ["col", "paths"]}}}}`), []any{withItems})

	check("outgoing join", both("outgoing join", `{"relation": "ext_item", "schema": "public",
		"select": {"label": ["col", "label"], "owner": [".", "owner"], "hop": [".", "owner", "paths"]},
		"where": ["in", ["col", "label"], "Ext 3 two", "Ext 4 under empty", "Ext 5 under null"], "order_by": [["col", "label"]],
		"join": {"owner": {"relation": "ext_owner", "schema": "public", "on": {"id": "owner_id"}, "select": {`+cols+`}}}}`),
		[]any{map[string]any{"label": "Ext 3 two", "owner": full, "hop": []any{"a.b", "c"}},
			map[string]any{"label": "Ext 4 under empty", "owner": empty, "hop": []any{}},
			map[string]any{"label": "Ext 5 under null", "owner": null, "hop": nil}})

	check("expressions", both("expressions", `{"relation": "ext_owner", "schema": "public", "where": ["=", ["col", "label"], "Ext Owner"],
		"select": {"get": ["get", "label"], "sub": [".", "pair", "path"], "index": ["index", ["col", "paths"], 1], "slice": ["slice", ["col", "paths"], 2, 2],
			"coalesce": ["coalesce", ["col", "path"], ["col", "path"]], "computed": [".", "ext_owner_root"], "call": ["call", "ext_root_path"]}}`),
		[]any{map[string]any{"get": "Ext Owner", "sub": "p.q", "index": "a.b", "slice": []any{"c"}, "coalesce": "a.b", "computed": "a", "call": "r.s"}})

	// a write without joins : the fast path's read-back
	fast := postAccepting(t, testHandler, `{"query": {"relation": "ext_item", "schema": "public", "write_mode": "insert",
		"select": {"id": ["col", "id"], "label": ["col", "label"], "paths": ["col", "paths"]}}, "data": [{"label": "Ext fast", "paths": ["f.g"]}]}`, "application/cbor")
	if fast.Code != http.StatusOK {
		t.Fatalf("fast write : status %d : %s", fast.Code, fast.Body.String())
	}
	fastRows := decodeCBOR[[]map[string]any](t, fast.Body.Bytes())
	if len(fastRows) == 1 {
		delete(fastRows[0], "id")
	}
	check("fast write read-back", fastRows, []map[string]any{{"label": "Ext fast", "paths": []any{"f.g"}}})

	fn := postAccepting(t, testHandler, `{"function": "ext_root_path", "schema": "public"}`, "application/cbor")
	if fn.Code != http.StatusOK {
		t.Fatalf("function root : status %d : %s", fn.Code, fn.Body.String())
	}
	check("function root", decodeCBOR[any](t, fn.Body.Bytes()), "r.s")
}

// distinct compares the values the columns hold, whatever the response's form : citext's 'Dist A' and 'dist a' are
// one row in CBOR as in JSON, and an order_by on the column stays valid. Which of the two is answered is Postgres'
// choice, so labels are compared lowercased.
func TestRelHandler_DistinctComparesColumnValues(t *testing.T) {
	ctx := t.Context()
	var owner int
	if err := testDb.Pool.QueryRow(ctx, `insert into ext_owner (label, path) values ('Dist A', 'a'), ('dist a', 'a'), ('Dist B', 'b') returning id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if _, err := testDb.Pool.Exec(ctx, `insert into ext_item (owner_id, label) values ($1, 'Dup'), ($1, 'dup'), ($1, 'Single')`, owner); err != nil {
		t.Fatal(err)
	}
	var lower func(v any) any
	lower = func(v any) any {
		switch x := v.(type) {
		case string:
			return strings.ToLower(x)
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				out[i] = lower(e)
			}
			return out
		case map[string]any:
			out := map[string]any{}
			for k, e := range x {
				out[k] = lower(e)
			}
			return out
		}
		return v
	}
	where := `"where": ["like", ["col", "label"], "Dist %"]`
	for name, c := range map[string]struct{ body, want string }{
		"root": {`{"relation": "ext_owner", "schema": "public", "distinct": true, "select": {"label": ["col", "label"], "path": ["col", "path"]}, ` + where + `,
			"order_by": [["desc", ["col", "label"]]], "limit": 5}`, `[{"label": "dist b", "path": "b"}, {"label": "dist a", "path": "a"}]`},
		"scalar": {`{"relation": "ext_owner", "schema": "public", "distinct": true, "select": ["col", "label"], ` + where + `, "order_by": [["col", "label"]]}`,
			`["dist a", "dist b"]`},
		"join": {fmt.Sprintf(`{"relation": "ext_owner", "schema": "public", "select": {"items": [".", "items"]}, "where": ["=", ["col", "id"], %d],
			"join": {"items": {"relation": "ext_item", "schema": "public", "on": {"owner_id": "id"}, "distinct": true, "order_by": [["col", "label"]],
				"select": {"label": ["col", "label"]}}}}`, owner), `[{"items": [{"label": "dup"}, {"label": "single"}]}]`},
	} {
		j := postAccepting(t, testHandler, c.body, "")
		cb := postAccepting(t, testHandler, c.body, "application/cbor")
		if j.Code != http.StatusOK || cb.Code != http.StatusOK {
			t.Fatalf("%s : status JSON %d, CBOR %d : %s %s", name, j.Code, cb.Code, j.Body.String(), cb.Body.String())
		}
		var want any
		if err := json.Unmarshal([]byte(c.want), &want); err != nil {
			t.Fatal(err)
		}
		if got := lower(decodeJSON[any](t, j.Body.Bytes())); !reflect.DeepEqual(got, want) {
			t.Errorf("%s JSON : %#v, want %#v", name, got, want)
		}
		if got := lower(decodeCBOR[any](t, cb.Body.Bytes())); !reflect.DeepEqual(got, want) {
			t.Errorf("%s CBOR : %#v, want %#v", name, got, want)
		}
	}
}
