package query

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func rowByID(table []map[string]any, id int) map[string]any {
	for _, r := range table {
		if r["id"] == float64(id) {
			return r
		}
	}
	return nil
}

func sameValue(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

// TestWrite_AbsentVersusNull checks what a column a row leaves out, sets to null, or supplies becomes in each
// write mode, through both paths (the general temp table, and the fast path where it applies).
func TestWrite_AbsentVersusNull(t *testing.T) {
	const sel = `"select": {"id": ["col", "id"], "a": ["col", "a"], "b": ["col", "b"], "c": ["col", "c"], "d": ["col", "d"]}`
	existing := []string{`insert into defaults_t (id, a, b, c, d) values (1, 1, 2, 3, 'old'), (2, 10, 20, 30, 'old2')`}
	cases := []struct {
		name    string
		mode    string
		setup   []string
		payload string
		want    map[int]map[string]any // expected rows, by id
	}{
		{"insert, everything absent takes the defaults", "insert", nil, `[{"id": 1}]`,
			map[int]map[string]any{1: {"a": 5.0, "b": 6.0, "c": nil, "d": "x"}}},
		{"insert, null in a nullable column stays null", "insert", nil, `[{"id": 1, "a": null, "d": null}]`,
			map[int]map[string]any{1: {"a": nil, "b": 6.0, "d": nil}}},
		{"insert, null in a NOT NULL column takes the default", "insert", nil, `[{"id": 1, "b": null}]`,
			map[int]map[string]any{1: {"b": 6.0}}},
		{"insert, every column supplied", "insert", nil, `[{"id": 1, "a": 9, "b": 8, "c": 7, "d": "y"}]`,
			map[int]map[string]any{1: {"a": 9.0, "b": 8.0, "c": 7.0, "d": "y"}}},
		{"insert, rows supplying different columns", "insert", nil, `[{"id": 1, "a": 9}, {"id": 2, "c": 4}, {"id": 3}]`,
			map[int]map[string]any{1: {"a": 9.0, "b": 6.0, "c": nil}, 2: {"a": 5.0, "c": 4.0}, 3: {"a": 5.0, "d": "x"}}},
		{"update-existing, an absent column keeps its value", "update-existing", existing, `[{"id": 1, "c": 99}]`,
			map[int]map[string]any{1: {"a": 1.0, "b": 2.0, "c": 99.0, "d": "old"}, 2: {"a": 10.0, "c": 30.0}}},
		{"update-existing, null sets NULL in a nullable column", "update-existing", existing, `[{"id": 1, "a": null}]`,
			map[int]map[string]any{1: {"a": nil, "b": 2.0, "c": 3.0}}},
		{"update-existing, null in a NOT NULL column takes the default", "update-existing", existing, `[{"id": 1, "b": null}]`,
			map[int]map[string]any{1: {"a": 1.0, "b": 6.0}}},
		{"update-existing, rows supplying different columns", "update-existing", existing, `[{"id": 1, "a": 100}, {"id": 2, "d": "new"}]`,
			map[int]map[string]any{1: {"a": 100.0, "b": 2.0, "d": "old"}, 2: {"a": 10.0, "b": 20.0, "d": "new"}}},
		{"upsert, a new row takes the defaults", "upsert", existing, `[{"id": 3, "c": 5}]`,
			map[int]map[string]any{3: {"a": 5.0, "b": 6.0, "c": 5.0, "d": "x"}, 1: {"a": 1.0}}},
		// documented in writing.md : an upserted row is prepared like an insert, so what it leaves out is overwritten
		{"upsert, an existing row gets what an insert would have given it", "upsert", existing, `[{"id": 1, "c": 9}]`,
			map[int]map[string]any{1: {"a": 5.0, "b": 6.0, "c": 9.0, "d": "x"}, 2: {"a": 10.0}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			node := mustResolveQuery(t, `{"relation": "defaults_t", "schema": "public", `+sel+`, "write_mode": "`+c.mode+`"}`)
			_, _, generalTable, fastTable := runBothWays(t, c.setup, node, c.payload, "defaults_t")
			for label, table := range map[string][]map[string]any{"general": generalTable, "fast": fastTable} {
				for id, want := range c.want {
					got := rowByID(table, id)
					if got == nil {
						t.Errorf("%s path: no row %d in %v", label, id, table)
						continue
					}
					for col, v := range want {
						if !sameValue(got[col], v) {
							t.Errorf("%s path: row %d column %s = %v, want %v", label, id, col, got[col], v)
						}
					}
				}
			}
		})
	}
}

// TestWrite_MoreThanSixtyFourSlots : the presence bits of a table with 71 slots span two mask words.
func TestWrite_MoreThanSixtyFourSlots(t *testing.T) {
	node := mustResolveQuery(t, `{"relation": "wide_t", "schema": "public", "select": ["*~"], "write_mode": "insert"}`)
	payload := `[
		{"id": 1, "c01": 1, "c03": 3, "c67": 67, "c68": 680, "c69": 69, "c70": "seventy"},
		{"id": 2, "c02": 2},
		{"id": 3, "c68": null, "c05": null, "c64": 64, "c65": 65}
	]`
	general, fast, generalTable, fastTable := runBothWays(t, nil, node, payload, "wide_t")
	if len(general) != 3 || len(fast) != 3 {
		t.Fatalf("responses: %d and %d rows", len(general), len(fast))
	}
	want := map[int]map[string]any{
		1: {"c01": 1.0, "c02": nil, "c03": 3.0, "c05": 5.0, "c67": 67.0, "c68": 680.0, "c69": 69.0, "c70": "seventy"},
		2: {"c02": 2.0, "c05": 5.0, "c68": 68.0, "c70": nil},
		3: {"c05": nil, "c64": 64.0, "c65": 65.0, "c68": 68.0},
	}
	for label, table := range map[string][]map[string]any{"general": generalTable, "fast": fastTable} {
		for id, cols := range want {
			for col, v := range cols {
				if got := rowByID(table, id)[col]; !sameValue(got, v) {
					t.Errorf("%s path: row %d %s = %v, want %v", label, id, col, got, v)
				}
			}
		}
	}
}

// TestWrite_MetadataNamesThatClashWithColumns : the temp table avoids the names the table's own columns use.
func TestWrite_MetadataNamesThatClashWithColumns(t *testing.T) {
	node := mustResolveQuery(t, `{"relation": "clash_t", "schema": "public", "select": ["*~"], "write_mode": "insert"}`)
	tt, err := newTempTable(node.Relation)
	if err != nil {
		t.Fatal(err)
	}
	if tt.prefix != "__rel___" {
		t.Errorf("prefix = %q, want __rel___ (columns start with __rel_ and __rel__)", tt.prefix)
	}
	_, _, generalTable, fastTable := runBothWays(t, nil, node,
		`[{"id": 1, "__rel_row_id": "mine", "name": "n"}, {"id": 2, "__rel__mask0": "other"}]`, "clash_t")
	for label, table := range map[string][]map[string]any{"general": generalTable, "fast": fastTable} {
		r1, r2 := rowByID(table, 1), rowByID(table, 2)
		if r1["__rel_row_id"] != "mine" || r1["__rel_mask0"] != 7.0 || r1["name"] != "n" {
			t.Errorf("%s path: row 1 = %v", label, r1)
		}
		if r2["__rel__mask0"] != "other" || r2["__rel_row_id"] != nil {
			t.Errorf("%s path: row 2 = %v", label, r2)
		}
	}
}

// TestWrite_SessionTimeZone : a timestamptz written without an offset is read in the connection's session zone.
func TestWrite_SessionTimeZone(t *testing.T) {
	ctx := context.Background()
	node := mustResolveQuery(t, `{"relation": "tz_t", "schema": "public", "select": ["*~"], "write_mode": "insert"}`)
	for _, zone := range []string{"UTC", "Europe/Paris", "America/New_York", "Asia/Kolkata"} {
		t.Run(zone, func(t *testing.T) {
			conn := acquireWriteConn(t)
			if _, err := conn.Exec(ctx, "set time zone '"+zone+"'"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = conn.Exec(context.Background(), "reset time zone") })
			if _, err := conn.Exec(ctx, "delete from tz_t"); err != nil {
				t.Fatal(err)
			}
			payload := `[{"id": 1, "at": "2026-10-25 02:30:00", "plain": "2026-10-25 02:30:00", "day": "2026-10-25"},
				{"id": 2, "at": "2026-03-29 02:30:00+02:00", "plain": "2026-03-29 02:30:00+02:00"},
				{"id": 3, "at": "2026-07-01T12:00:00Z"}]`
			if _, err := ExecuteWrite(ctx, conn, node, []byte(payload)); err != nil {
				t.Fatal(err)
			}
			// the expected values are parameters : a literal would be folded into the cached statement with the
			// zone in force when it was first prepared
			rows, err := conn.Query(ctx, `select t.id, t.at::text, v.at::text, t.plain::text, v.plain::text, t.day::text, v.d::text
				from tz_t t join (values
				(1, $1::text::timestamptz, $2::text::timestamp, $3::text::date),
				(2, $4::text::timestamptz, $5::text::timestamp, null::date),
				(3, $6::text::timestamptz, null::timestamp, null::date)
				) v(id, at, plain, d) on v.id = t.id
				where t.at is distinct from v.at or t.plain is distinct from v.plain or t.day is distinct from v.d`,
				"2026-10-25 02:30:00", "2026-10-25 02:30:00", "2026-10-25",
				"2026-03-29 02:30:00+02:00", "2026-03-29 02:30:00+02:00", "2026-07-01T12:00:00Z")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var id int
				var gotAt, wantAt, gotPlain, wantPlain, gotDay, wantDay *string
				if err := rows.Scan(&id, &gotAt, &wantAt, &gotPlain, &wantPlain, &gotDay, &wantDay); err != nil {
					t.Fatal(err)
				}
				t.Errorf("row %d: at %v want %v, plain %v want %v, day %v want %v", id, deref(gotAt), deref(wantAt), deref(gotPlain), deref(wantPlain), deref(gotDay), deref(wantDay))
			}
		})
	}
}

func deref(s *string) string {
	if s == nil {
		return "NULL"
	}
	return *s
}

// TestWrite_SessionTimeZoneGoCannotLoad : an offset still places a time when the zone is unknown, a time without one cannot be.
func TestWrite_SessionTimeZoneGoCannotLoad(t *testing.T) {
	ctx := context.Background()
	node := mustResolveQuery(t, `{"relation": "tz_t", "schema": "public", "select": ["*~"], "write_mode": "insert"}`)
	conn := acquireWriteConn(t)
	if _, err := conn.Exec(ctx, "set time zone 'EST5'"); err != nil { // a POSIX zone : Postgres reads it, Go has no file for it
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), "reset time zone") })
	if _, err := ExecuteWrite(ctx, conn, node, []byte(`[{"id": 10, "at": "2026-07-01T12:00:00+02:00"}]`)); err != nil {
		t.Errorf("a time with an offset: %v", err)
	}
	_, err := ExecuteWrite(ctx, conn, node, []byte(`[{"id": 11, "at": "2026-07-01 12:00:00"}]`))
	if err == nil || !strings.Contains(err.Error(), "EST5") {
		t.Errorf("a time without an offset: err = %v, want a refusal naming the zone", err)
	}
}

// TestWrite_EveryKindOfColumn writes whole rows of enc_t, the table of arrays, composites and types that
// only have a text form, and compares what is stored with what jsonb_populate_record makes of the same JSON.
func TestWrite_EveryKindOfColumn(t *testing.T) {
	ctx := context.Background()
	node := mustResolveQuery(t, `{"relation": "enc_t", "schema": "public", "select": ["*~"], "write_mode": "insert"}`)
	rows := []string{
		`{"id": 1, "i4s": [1,2,3], "txts": ["a","b c","d,e"], "nums": [1.234, null], "uuids": ["a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"],
		  "tss": ["2026-10-18T14:30:00Z"], "bools": [true, "off"], "jsonbs": [{"a":1}, [1,2]], "jsons": [[1,2],[3,4]],
		  "inner_c": {"a": 1, "b": "x"}, "outer_c": {"i": {"a": 2}, "tags": ["q"], "n": 1.5, "t": "2026-10-18T14:30:00+02:00", "j": {"k": 1}},
		  "inners": [{"a":1},{"b":"y"}], "outers": [{"n": 2}], "iv": "1 day 02:00:00", "ivs": ["1 day", "2 hours"],
		  "ivc": {"code": "a,b", "span": "3 days"}, "ivcs": [{"code": "x"}, null], "pos": 5, "poss": [1,2], "mood": "ok", "moods": ["sad"],
		  "ip": "10.0.0.1/24", "ips": ["::1"], "rng": "[1,5)", "rngs": ["empty"], "pt": "(1,2)", "tsv": "a b", "cash": "$12.50",
		  "bits": "1010", "vc": "abc", "chr": "ab", "bytes": "\\x00ff", "byteas": ["\\x01", null]}`,
		`{"id": 2}`,
		`{"id": 3, "i4s": null, "inner_c": null, "iv": null, "ivs": null}`,
		`{"id": 4, "i4s": [], "txts": [], "inners": [], "ivs": [], "ivcs": [{"code": "say \"hi\" (really), {ok}"}]}`,
	}
	for i, src := range rows {
		conn := acquireWriteConn(t)
		if _, err := conn.Exec(ctx, "delete from enc_t"); err != nil {
			t.Fatal(err)
		}
		if _, err := ExecuteWrite(ctx, conn, node, []byte("["+src+"]")); err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		var ok bool
		var stored, oracle string
		err := conn.QueryRow(ctx, `select to_jsonb(e) = to_jsonb(jsonb_populate_record(null::enc_t, $1::jsonb)), to_jsonb(e)::text, to_jsonb(jsonb_populate_record(null::enc_t, $1::jsonb))::text from enc_t e`, src).Scan(&ok, &stored, &oracle)
		if err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		if !ok {
			t.Errorf("row %d: stored %s\n         oracle %s", i, stored, oracle)
		}
	}
}

// TestWrite_NotAnObjectRow : a row of the payload that is not an object is refused, instead of writing an empty row.
func TestWrite_NotAnObjectRow(t *testing.T) {
	conn := acquireWriteConn(t)
	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}`)
	for _, payload := range []string{`[1]`, `["x"]`, `[[{"name": "n"}]]`, `[null]`} {
		if _, err := ExecuteWrite(context.Background(), conn, node, []byte(payload)); err == nil {
			t.Errorf("payload %s was accepted", payload)
		}
	}
}
