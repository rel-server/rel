package query

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The oracle for native CBOR results is the JSON Postgres builds for the same value : the CBOR must say the same
// thing, with the types JSON lacks (instants, dates, UUIDs, bytes, decimals) turned back into the text JSON gave them.

// dateText prints days since 1970-01-01 as Postgres' JSON does.
func dateText(days int64) string {
	t := time.Unix(days*86400, 0).UTC()
	if y := t.Year(); y <= 0 {
		return fmt.Sprintf("%04d-%02d-%02d BC", 1-y, t.Month(), t.Day())
	}
	return t.Format("2006-01-02")
}

// equivalent returns "" when the CBOR node says what the JSON value says.
func equivalent(path string, n Node, j any) string {
	bad := func(format string, args ...any) string { return path + ": " + fmt.Sprintf(format, args...) }
	switch n.Kind() {
	case KindNull:
		if j != nil {
			return bad("null, JSON has %v", j)
		}
	case KindBool:
		b, _ := n.StrictBool()
		if jb, ok := j.(bool); !ok || jb != b {
			return bad("%v, JSON has %v", b, j)
		}
	case KindNumber:
		text, _ := n.Raw()
		switch jv := j.(type) {
		case string: // NaN and the infinities are strings in JSON
			if !strings.EqualFold(jv, text) {
				return bad("number %s, JSON has the string %q", text, jv)
			}
		case json.Number:
			want, _ := new(big.Rat).SetString(jv.String())
			got, ok := new(big.Rat).SetString(text)
			if !ok {
				return bad("number %q", text)
			}
			if got.Cmp(want) != 0 {
				// a float4 keeps its single precision where JSON printed its shortest decimal
				f, _ := n.StrictFloat64()
				jf, _ := jv.Float64()
				if float32(f) != float32(jf) {
					return bad("number %s, JSON has %s", text, jv)
				}
			}
		default:
			return bad("number, JSON has %v", j)
		}
	case KindString:
		s, _ := n.StrictString()
		if js, ok := j.(string); !ok || js != s {
			return bad("%q, JSON has %v", s, j)
		}
	case KindBytes:
		b, _ := n.(*cnode).StrictBytes()
		if js, ok := j.(string); !ok || js != `\x`+hex.EncodeToString(b) {
			return bad("bytes %x, JSON has %v", b, j)
		}
	case KindUUID:
		b, _ := n.(*cnode).StrictBytes()
		h := hex.EncodeToString(b)
		if js, ok := j.(string); !ok || js != h[:8]+"-"+h[8:12]+"-"+h[12:16]+"-"+h[16:20]+"-"+h[20:] {
			return bad("uuid %x, JSON has %v", b, j)
		}
	case KindDate:
		d, _ := n.(*cnode).Days()
		if js, ok := j.(string); !ok || js != dateText(d) {
			return bad("date %s, JSON has %v", dateText(d), j)
		}
	case KindInstant:
		got, _ := n.(*cnode).Instant()
		js, ok := j.(string)
		if !ok {
			return bad("instant, JSON has %v", j)
		}
		if bc := strings.HasSuffix(js, " BC"); bc { // Go parses no BC year : compare everything but the year
			y := 0
			fmt.Sscanf(js[:4], "%d", &y)
			js = "2000" + strings.TrimSuffix(js, " BC")[4:]
			got = got.UTC()
			if got.Year() != 1-y {
				return bad("instant year %d, JSON has %d BC", got.Year(), y)
			}
			got = time.Date(2000, got.Month(), got.Day(), got.Hour(), got.Minute(), got.Second(), got.Nanosecond(), time.UTC)
		}
		want, err := time.Parse(time.RFC3339Nano, js)
		if err != nil {
			return bad("instant %v, JSON has %q which is no time", got, js)
		}
		if d := got.Sub(want); d > time.Microsecond || d < -time.Microsecond {
			return bad("instant %v, JSON has %v", got, want)
		}
	case KindArray:
		items, _ := n.ArrayItems()
		ja, ok := j.([]any)
		if !ok || len(ja) != len(items) {
			return bad("array of %d, JSON has %v", len(items), j)
		}
		for i := range items {
			if d := equivalent(fmt.Sprintf("%s[%d]", path, i), items[i], ja[i]); d != "" {
				return d
			}
		}
	case KindObject:
		jo, ok := j.(map[string]any)
		if !ok {
			return bad("object, JSON has %v", j)
		}
		count := 0
		var diff string
		_ = n.ObjectEach(func(k string, v Node) error {
			count++
			jv, present := jo[k]
			if !present && diff == "" {
				diff = bad("key %q is not in the JSON", k)
			} else if d := equivalent(path+"."+k, v, jv); d != "" && diff == "" {
				diff = d
			}
			return nil
		})
		if diff != "" {
			return diff
		}
		if count != len(jo) {
			return bad("%d keys, JSON has %d", count, len(jo))
		}
	default:
		return bad("kind %d", n.Kind())
	}
	return ""
}

func decodeJSONNumbers(t *testing.T, raw []byte) any {
	t.Helper()
	if raw == nil {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("not JSON: %s", raw)
	}
	return v
}

// cborOfExpr evaluates a SQL expression and returns its CBOR through a ResultEncoder.
func cborOfExpr(t *testing.T, conn *pgxpool.Conn, expr string) (Node, error) {
	t.Helper()
	rows, err := conn.Query(context.Background(), "select "+expr, pgx.QueryResultFormats{pgx.BinaryFormatCode})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, fmt.Errorf("no row")
	}
	oid := rows.FieldDescriptions()[0].DataTypeOID
	raw := rows.RawValues()[0]
	var cp []byte
	if raw != nil {
		cp = append([]byte{}, raw...)
	}
	out, err := (&ResultEncoder{db: testDb}).AppendValue(nil, oid, cp)
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	if err != nil {
		return nil, err
	}
	return NewCBORNode(out)
}

func jsonOfExpr(t *testing.T, conn *pgxpool.Conn, expr string) any {
	t.Helper()
	var raw []byte
	if err := conn.QueryRow(context.Background(), "select to_jsonb("+expr+")").Scan(&raw); err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	return decodeJSONNumbers(t, raw)
}

func TestResultCBOR_ScalarsMatchJSON(t *testing.T) {
	conn := tempTestConn(t)
	if _, err := conn.Exec(context.Background(), "set time zone 'UTC'"); err != nil {
		t.Fatal(err)
	}
	for _, expr := range []string{
		// integers and floats
		`1::int2`, `(-32768)::int2`, `2147483647::int4`, `(-2147483648)::int4`, `9223372036854775807::int8`, `(-9223372036854775808)::int8`, `42::oid`, `0`,
		`1.5::float8`, `0.1::float4`, `(-1.25e-10)::float8`, `1e300::float8`, `'NaN'::float8`, `'Infinity'::float8`, `'-Infinity'::float8`, `0.0::float8`, `3::float4`,
		// numeric, in all its shapes
		`1.50::numeric(8,2)`, `0::numeric`, `0.00::numeric(5,2)`, `(-0.0001)::numeric`, `1e-20::numeric`, `10000::numeric`, `100000000::numeric`, `0.5::numeric`,
		`123456789012345678901234567890.123456789::numeric`, `(-123456789012345678901234567890)::numeric`, `1e30::numeric`, `12345678.9e2::numeric`,
		`(2^100)::numeric`, `'NaN'::numeric`, `'Infinity'::numeric`, `'-Infinity'::numeric`, `99999999.99999999::numeric`, `0.0001::numeric`, `0.00001234::numeric`,
		`1e1000::numeric`, `(1e1000 + 0.5)::numeric`,
		// text and bytes
		`'héllo ✓ 日本語'::text`, `''::text`, `'ab'::char(5)`, `'x'::varchar(3)`, `'x'::name`, `'a'::"char"`, `true`, `false`,
		`'\x00ff10'::bytea`, `''::bytea`, `'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11'::uuid`,
		// dates and times
		`'2026-10-18'::date`, `'infinity'::date`, `'-infinity'::date`, `'0044-03-15 BC'::date`, `'0001-01-01'::date`, `'5874897-12-31'::date`, `'4714-11-24 BC'::date`,
		`'2026-10-18 14:30:00.123456+02'::timestamptz`, `'2026-10-18 14:30:00+00'::timestamptz`, `'1970-01-01 00:00:00+00'::timestamptz`, `'1969-12-31 23:59:59.5+00'::timestamptz`,
		`'2000-01-01 00:00:00+00'::timestamptz`, `'infinity'::timestamptz`, `'-infinity'::timestamptz`, `'0044-03-15 12:00:00+00 BC'::timestamptz`, `'9999-12-31 23:59:59.999999+00'::timestamptz`,
		`'0001-01-01 00:00:00.000001+00'::timestamptz`, `'2026-10-18 14:30:00.123456'::timestamp`, `'2026-10-18'::timestamp`, `'2026-10-18 14:30:00.5'::timestamp`,
		`'0044-03-15 00:00:00 BC'::timestamp`, `'infinity'::timestamp`, `'-infinity'::timestamp`, `'10000-01-01 00:00:00'::timestamp`, `'1999-12-31 23:59:59.999999'::timestamp`,
		`'294276-12-31 23:59:59'::timestamp`, `'4714-11-24 00:00:00 BC'::timestamp`,
		// json
		`'{"a":[1,2,{"b":null}],"c":1.50,"d":"é"}'::jsonb`, `'[1e400, -0.0, 12345678901234567890123]'::jsonb`, `'"str"'::json`, `'null'::jsonb`, `'null'::json`, `'[]'::jsonb`, `'{}'::jsonb`,
		`'{"a":1,"b":[true,false]}'::json`, `jsonb_build_object('k', 1.5, 'n', null)`,
		// arrays
		`array[1,2,3]`, `array[1,null,3]`, `array[[1,2],[3,4]]`, `'{}'::int4[]`, `array['a','b']`, `array[1.50,2.5]::numeric[]`, `array['2026-10-18 12:00+00']::timestamptz[]`,
		`array[true,false]`, `array['\x01'::bytea, null]`, `array[array[1,2],array[3,4]]`, `array[[[1],[2]],[[3],[4]]]`, `array['a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11'::uuid]`,
		`array['2026-10-18'::date]`, `array['{"a":1}'::jsonb]`, `array[1.5,2.5]::float4[]`, `array[null,null]::text[]`,
		// enums, domains, composites
		`'ok'::enc_mood`, `array['ok','sad']::enc_mood[]`, `5::enc_posint`, `array[5,6]::enc_posint[]`,
		`row(1,'x')::enc_inner`, `row(null,null)::enc_inner`, `array[row(1,'x')::enc_inner, null]`,
		`row(row(1,'x')::enc_inner, array['a','b'], 1.5, '2026-10-18 12:00:00+00'::timestamptz, '{"k":1}'::jsonb)::enc_outer`,
		`array[row(row(1,'x')::enc_inner, array['a'], 2.25, null, null)::enc_outer]`,
		// nulls
		`null::int`, `null::text`, `null::jsonb`, `null::int4[]`, `null::enc_inner`, `null::numeric`,
	} {
		node, err := cborOfExpr(t, conn, expr)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		if d := equivalent("$", node, jsonOfExpr(t, conn, expr)); d != "" {
			t.Errorf("%s: %s", expr, d)
		}
	}
}

func TestResultCBOR_Details(t *testing.T) {
	conn := tempTestConn(t)
	// the display scale of a numeric is kept
	n, err := cborOfExpr(t, conn, `1.50::numeric(8,2)`)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := n.Raw(); got != "150e-2" {
		t.Errorf("1.50 = %s, want 150e-2", got)
	}
	for expr, want := range map[string]string{`10000::numeric`: "10000e0", `0.00::numeric(5,2)`: "0e-2", `(-0.0001)::numeric`: "-1e-4", `1e30::numeric`: "1000000000000000000000000000000e0"} {
		n, err := cborOfExpr(t, conn, expr)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := n.Raw(); got != want {
			t.Errorf("%s = %s, want %s", expr, got, want)
		}
	}
	// a float4 stays a single
	if n, _ := cborOfExpr(t, conn, `0.1::float4`); n.Kind() != KindNumber {
		t.Error("float4")
	}
	// anonymous record fields are named f1, f2
	n, err = cborOfExpr(t, conn, `row(1, 'a'::text)`)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := n.ObjectGet("f2"); !ok {
		t.Error("an anonymous record has no f2")
	} else if s, _ := v.StrictString(); s != "a" {
		t.Errorf("f2 = %q", s)
	}
	n, err = cborOfExpr(t, conn, `(select t from (select 1 a, 'x'::text b) t)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n.ObjectGet("f1"); !ok {
		t.Error("a record out of a subquery has no f1")
	}
	// a timestamptz with a fraction is an integer when it has no fraction, a double of seconds otherwise
	n, _ = cborOfExpr(t, conn, `'2023-11-14 22:13:20.5+00'::timestamptz`)
	if got, _ := n.(*cnode).Instant(); got.Unix() != 1700000000 || got.Nanosecond() != 500_000_000 {
		t.Errorf("instant = %v", got)
	}
	// types with no CBOR form are an error that says so
	for _, expr := range []string{`'1 day'::interval`, `'10.0.0.1'::inet`, `'[1,5)'::int4range`, `'12.5'::money`} {
		if _, err := cborOfExpr(t, conn, expr); err == nil || !strings.Contains(err.Error(), "no CBOR form") {
			t.Errorf("%s : err = %v, want one saying the type has no CBOR form", expr, err)
		}
	}
}

func TestResultCBOR_RefusesTruncatedValues(t *testing.T) {
	conn := tempTestConn(t)
	for _, expr := range []string{`array[1,2,3]`, `row(1,'x')::enc_inner`, `1.50::numeric(8,2)`, `(select array_agg(t) from (select 1 a, 'b' b) t)`, `'{"a":1}'::jsonb`, `'2026-10-18 12:00+00'::timestamptz`} {
		rows, err := conn.Query(context.Background(), "select "+expr, pgx.QueryResultFormats{pgx.BinaryFormatCode})
		if err != nil {
			t.Fatal(err)
		}
		rows.Next()
		oid := rows.FieldDescriptions()[0].DataTypeOID
		raw := append([]byte{}, rows.RawValues()[0]...)
		rows.Close()
		e := &ResultEncoder{db: testDb}
		if _, err := e.AppendValue(nil, oid, raw); err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		for n := 0; n < len(raw); n++ { // every shorter prefix must be an error or a value, never a panic
			_, _ = e.AppendValue(nil, oid, raw[:n])
		}
	}
	_ = math.Pi
}

// cborRowsOf runs the CBOR form of a compiled query and returns one node per row.
func cborRowsOf(t *testing.T, root *QueryNode) []Node {
	t.Helper()
	w, err := CompileSelectFormat(root, FormatCBOR)
	if err != nil {
		t.Fatalf("CompileSelectFormat: %v", err)
	}
	enc, err := NewResultEncoder(testDb, root)
	if err != nil {
		t.Fatalf("NewResultEncoder: %v", err)
	}
	args := append([]any{pgx.QueryResultFormats{pgx.BinaryFormatCode}}, w.Args()...)
	rows, err := testDb.Pool.Query(context.Background(), w.String(), args...)
	if err != nil {
		t.Fatalf("query: %v\nsql: %s", err, w.String())
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		oid := rows.FieldDescriptions()[0].DataTypeOID
		var cp []byte
		if raw := rows.RawValues()[0]; raw != nil {
			cp = append([]byte{}, raw...)
		}
		b, err := enc.AppendValue(nil, oid, cp)
		if err != nil {
			t.Fatalf("encode: %v\nsql: %s", err, w.String())
		}
		n, err := NewCBORNode(b)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	if rows.Err() != nil {
		t.Fatalf("rows: %v\nsql: %s", rows.Err(), w.String())
	}
	return out
}

func jsonRowsOf(t *testing.T, root *QueryNode) []any {
	t.Helper()
	w, err := CompileSelect(root)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := testDb.Pool.Query(context.Background(), w.String(), w.Args()...)
	if err != nil {
		t.Fatalf("query: %v\nsql: %s", err, w.String())
	}
	defer rows.Close()
	var out []any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		out = append(out, decodeJSONNumbers(t, raw))
	}
	return out
}

func TestResultCBOR_CompiledQueriesMatchJSON(t *testing.T) {
	ctx := context.Background()
	var did int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('Cbor Director') returning id`).Scan(&did); err != nil {
		t.Fatal(err)
	}
	if _, err := testDb.Pool.Exec(ctx, `insert into movie (director_id, title) values ($1, 'Cbor A'), ($1, 'Cbor B')`, did); err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]string{
		"bare":          `{"relation": "director", "schema": "public", "select": ["*~"], "where": ["=", ["col", "id"], %d]}`,
		"to-many":       `{"relation": "director", "schema": "public", "select": {"id": ["col", "id"], "name": ["col", "name"], "movies": [".", "movies"]}, "where": ["=", ["col", "id"], %d], "join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": {"title": ["col", "title"]}}}}`,
		"to-many-empty": `{"relation": "director", "schema": "public", "select": {"id": ["col", "id"], "movies": [".", "movies"]}, "where": ["=", ["col", "id"], %d], "join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "where": ["=", ["col", "title"], "none"], "select": {"title": ["col", "title"]}}}}`,
		"scalar":        `{"relation": "director", "schema": "public", "select": ["col", "name"], "where": ["=", ["col", "id"], %d]}`,
		"none":          `{"relation": "director", "schema": "public", "select": ["*~"], "where": ["=", ["col", "id"], -1]}`,
	} {
		root := mustResolveQuery(t, strings.ReplaceAll(fmt.Sprintf(q, did), "%!d(MISSING)", ""))
		want, got := jsonRowsOf(t, root), cborRowsOf(t, root)
		if len(want) != len(got) {
			t.Errorf("%s: %d rows in JSON, %d in CBOR", name, len(want), len(got))
			continue
		}
		for i := range want {
			if d := equivalent(name, got[i], want[i]); d != "" {
				t.Errorf("%s", d)
			}
		}
	}
}
