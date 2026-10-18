package query

import (
	"context"
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The forms only a CBOR document has : byte strings, tags 0, 1, 4, 37 and 100, floats of every width,
// bignums. (The forms both formats share are replayed from the JSON corpora, see encSources.)

// renderCBOR encodes n for typeName and has Postgres print the value back.
func renderCBOR(t *testing.T, conn *pgxpool.Conn, ec *encodeContext, typeName string, enc fieldEncoder, n Node) (string, error) {
	t.Helper()
	field, err := enc(nil, n, ec)
	if err != nil {
		return "", err
	}
	if field == nil {
		field = []byte{}
	}
	return pgRender(conn, pgTypeOID(t, conn, typeName), field)
}

type cborCase struct {
	name string
	v    any
	want string // the value read back, or "" for an error
}

func runCBORCases(t *testing.T, typeName string, enc fieldEncoder, cases []cborCase) {
	t.Helper()
	conn, ec := encTestConn(t, "UTC")
	for _, c := range cases {
		got, err := renderCBOR(t, conn, ec, typeName, enc, cborOf(t, c.v))
		switch {
		case c.want == "" && err == nil:
			t.Errorf("%s %s: accepted, read back as %q", typeName, c.name, got)
		case c.want != "" && err != nil:
			t.Errorf("%s %s: %v", typeName, c.name, err)
		case c.want != "" && got != c.want:
			t.Errorf("%s %s: read back as %q, want %q", typeName, c.name, got, c.want)
		}
	}
}

func TestEncodeCBOR_Bytes(t *testing.T) {
	runCBORCases(t, "bytea", encodeBytea, []cborCase{
		{"bytes", []byte{0, 255, 16}, `\x00ff10`}, {"empty", []byte{}, `\x`}, {"the hex text still works", `\xdead`, `\xdead`},
		{"a uuid tag", cbor.Tag{Number: 37, Content: make([]byte, 16)}, ""}, {"an integer", int64(1), ""},
	})
	uuid := []byte{0xa0, 0xee, 0xbc, 0x99, 0x9c, 0x0b, 0x4e, 0xf8, 0xbb, 0x6d, 0x6b, 0xb9, 0xbd, 0x38, 0x0a, 0x11}
	runCBORCases(t, "uuid", encodeUUID, []cborCase{
		{"tag 37", cbor.Tag{Number: 37, Content: uuid}, "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"},
		{"the text still works", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"},
		{"untagged bytes", uuid, ""}, {"tag 37 of the wrong length", cbor.Tag{Number: 37, Content: uuid[:15]}, ""},
	})
}

func TestEncodeCBOR_Dates(t *testing.T) {
	day := func(d int64) string { return time.Unix(d*86400, 0).UTC().Format("2006-01-02") }
	runCBORCases(t, "date", encodeDate, []cborCase{
		{"epoch", cbor.Tag{Number: 100, Content: int64(0)}, "1970-01-01"},
		{"2024-10-04", cbor.Tag{Number: 100, Content: int64(20000)}, day(20000)},
		{"before the epoch", cbor.Tag{Number: 100, Content: int64(-1)}, "1969-12-31"},
		{"year 1", cbor.Tag{Number: 100, Content: int64(-719162)}, "0001-01-01"},
		{"the last day", cbor.Tag{Number: 100, Content: int64(2145031948 + 10957)}, "5874897-12-31"},
		{"one past", cbor.Tag{Number: 100, Content: int64(2145031948 + 10957 + 1)}, ""},
		{"far out", cbor.Tag{Number: 100, Content: int64(1) << 50}, ""},
		{"the text still works", "2026-10-18", "2026-10-18"},
		{"a plain integer", int64(20000), ""}, {"an instant", time.Unix(1700000000, 0), ""},
	})
}

func TestEncodeCBOR_Instants(t *testing.T) {
	tag1 := func(secs any) cbor.Tag { return cbor.Tag{Number: 1, Content: secs} }
	runCBORCases(t, "timestamptz", encodeTimestamptz, []cborCase{
		{"tag 1, integer seconds", tag1(int64(1700000000)), "2023-11-14 22:13:20+00"},
		{"tag 1, half a second", tag1(1700000000.5), "2023-11-14 22:13:20.5+00"},
		{"tag 1, a microsecond", tag1(1700000000.123456), "2023-11-14 22:13:20.123456+00"},
		{"tag 1, rounded to the microsecond", tag1(1700000000.1234567), "2023-11-14 22:13:20.123457+00"},
		{"tag 1, before the epoch", tag1(-1.5), "1969-12-31 23:59:58.5+00"},
		{"tag 1, the epoch", tag1(int64(0)), "1970-01-01 00:00:00+00"},
		{"tag 1, year 2", tag1(time.Date(2, 1, 1, 0, 0, 0, 0, time.UTC).Unix()), "0002-01-01 00:00:00+00"},
		{"tag 1, the first valid instant", tag1(int64(-210866803200)), "4714-11-24 00:00:00+00 BC"},
		{"tag 1, one second before", tag1(int64(-210866803201)), ""},
		{"tag 1, the last second", tag1(time.Date(294276, 12, 31, 23, 59, 59, 0, time.UTC).Unix()), "294276-12-31 23:59:59+00"},
		{"tag 0, an offset", cbor.Tag{Number: 0, Content: "2026-10-18T14:30:00+02:00"}, "2026-10-18 12:30:00+00"},
		{"tag 1, out of range", tag1(int64(1) << 45), ""},
		{"the text still works", "2026-10-18T14:30:00Z", "2026-10-18 14:30:00+00"},
		{"a plain integer", int64(1700000000), ""},
	})
	// a timestamp takes an instant as the wall clock it shows in the session zone (UTC here)
	runCBORCases(t, "timestamp", encodeTimestamp, []cborCase{
		{"an instant", tag1(int64(1700000000)), "2023-11-14 22:13:20"},
		{"an instant with a fraction", tag1(1700000000.5), "2023-11-14 22:13:20.5"},
		{"the text", "2026-10-18T14:30:00", "2026-10-18 14:30:00"},
	})
}

// A timestamp written as an instant is the wall clock Postgres shows for it in the session zone, clock changes included.
func TestEncodeCBOR_TimestampInstantInZone(t *testing.T) {
	instants := []time.Time{
		time.Date(2026, 10, 18, 12, 30, 0, 123456000, time.UTC),
		time.Date(2026, 3, 29, 0, 59, 59, 0, time.UTC), time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC), // Europe/Paris springs forward
		time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC), time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC), // and falls back
		time.Date(2026, 4, 5, 15, 29, 59, 0, time.UTC), time.Date(1850, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	for _, zone := range []string{"UTC", "Europe/Paris", "America/New_York", "Asia/Kolkata", "Australia/Lord_Howe", "Pacific/Apia"} {
		conn, ec := encTestConn(t, zone)
		for _, at := range instants {
			var want string
			if err := conn.QueryRow(context.Background(), `select (to_timestamp($1::float8) at time zone $2)::text`, float64(at.UnixMicro())/1e6, zone).Scan(&want); err != nil {
				t.Fatal(err)
			}
			got, err := renderCBOR(t, conn, ec, "timestamp", encodeTimestamp, cborOf(t, cbor.Tag{Number: 1, Content: float64(at.UnixMicro()) / 1e6}))
			if err != nil || got != want {
				t.Errorf("%s %v : read back as %q (%v), want %q", zone, at, got, err, want)
			}
		}
	}
	// a session zone rel cannot load cannot place an instant
	_, err := encodeTimestamp(nil, cborOf(t, cbor.Tag{Number: 1, Content: int64(0)}), &encodeContext{zoneName: "Mars/Base", zoneErr: errors.New("unknown")})
	if err == nil || !strings.Contains(err.Error(), "Mars/Base") {
		t.Errorf("an unloadable zone : err = %v", err)
	}
}

func TestEncodeCBOR_FloatsAndBignums(t *testing.T) {
	huge := new(big.Int).Lsh(big.NewInt(1), 90)
	runCBORCases(t, "float8", encodeFloat(64), []cborCase{
		{"double", 1.5, "1.5"}, {"integer", int64(3), "3"}, {"NaN", math.NaN(), "NaN"}, {"+Inf", math.Inf(1), "Infinity"},
		{"-Inf", math.Inf(-1), "-Infinity"}, {"tag 4", cbor.Tag{Number: 4, Content: []any{int64(-1), int64(15)}}, "1.5"},
		{"a bignum", huge, "1.2379400392853803e+27"}, {"a byte string", []byte{1}, ""}, {"a map", map[string]any{}, ""},
	})
	runCBORCases(t, "float4", encodeFloat(32), []cborCase{
		{"a single", float32(0.1), "0.1"}, {"a double that does not fit", 1e39, ""}, {"half", 1.0, "1"},
	})
	runCBORCases(t, "numeric", encodeNumeric, []cborCase{
		{"float", 1.5, "1.5"}, {"integer-valued float keeps its scale", 3.0, "3.0"}, {"NaN", math.NaN(), "NaN"}, {"Infinity", math.Inf(1), "Infinity"},
		{"tag 4 keeps the scale", cbor.Tag{Number: 4, Content: []any{int64(-2), int64(150)}}, "1.50"},
		{"tag 4 with a bignum mantissa", cbor.Tag{Number: 4, Content: []any{int64(-3), huge}}, "1237940039285380274899124.224"},
		{"a bignum", huge, "1237940039285380274899124224"},
		{"a negative bignum", new(big.Int).Neg(huge), "-1237940039285380274899124224"},
		{"tag 4 with a positive exponent", cbor.Tag{Number: 4, Content: []any{int64(3), int64(12)}}, "12000"},
		{"uint64 max", uint64(math.MaxUint64), "18446744073709551615"},
	})
	runCBORCases(t, "int8", encodeInt(64), []cborCase{
		{"int64 min", int64(math.MinInt64), "-9223372036854775808"}, {"uint64 max", uint64(math.MaxUint64), ""}, {"a bignum", huge, ""},
		{"a float", 3.0, ""}, {"tag 4", cbor.Tag{Number: 4, Content: []any{int64(0), int64(5)}}, ""}, {"a byte string", []byte{1}, ""},
	})
	runCBORCases(t, "int2", encodeInt(16), []cborCase{{"in range", int64(-32768), "-32768"}, {"one past", int64(32768), ""}})
	runCBORCases(t, "bool", encodeBool, []cborCase{{"true", true, "true"}, {"0", int64(0), "false"}, {"2", int64(2), ""}, {"bytes", []byte{1}, ""}})
}

func TestEncodeCBOR_WrongKinds(t *testing.T) {
	for _, typ := range []struct {
		name string
		enc  fieldEncoder
	}{{"text", encodeText}, {"int4", encodeInt(32)}, {"date", encodeDate}, {"uuid", encodeUUID}, {"bool", encodeBool}, {"jsonb", encodeJSONB}} {
		for name, v := range map[string]any{
			"a byte string": []byte{1}, "an instant": time.Unix(1, 0), "an epoch date": cbor.Tag{Number: 100, Content: int64(1)},
			"an unknown tag": cbor.Tag{Number: 999, Content: "x"}, "tag 37": cbor.Tag{Number: 37, Content: make([]byte, 16)},
		} {
			if typ.name == "uuid" && name == "tag 37" {
				continue
			}
			if typ.name == "date" && name == "an epoch date" {
				continue
			}
			if _, err := typ.enc(nil, cborOf(t, v), &encodeContext{}); err == nil {
				t.Errorf("%s accepted %s", typ.name, name)
			}
		}
	}
	// the message says which tag
	_, err := encodeText(nil, cborOf(t, cbor.Tag{Number: 999, Content: "x"}), nil)
	if err == nil || !strings.Contains(err.Error(), "tag 999") {
		t.Errorf("error = %v, want one naming tag 999", err)
	}
	// and carries the SQLSTATE Postgres would have raised
	var pe *pgconn.PgError
	if _, err := encodeInt(32)(nil, cborOf(t, []byte{1}), nil); !errors.As(err, &pe) || pe.Code != "22P02" {
		t.Errorf("error = %v, want SQLSTATE 22P02", err)
	}
}

func TestEncodeCBOR_JSONColumns(t *testing.T) {
	runCBORCases(t, "jsonb", encodeJSONB, []cborCase{
		{"a map", map[string]any{"b": []any{true, nil, "x"}, "a": int64(1)}, `{"a": 1, "b": [true, null, "x"]}`},
		{"a decimal keeps its scale", cbor.Tag{Number: 4, Content: []any{int64(-2), int64(150)}}, "1.50"},
		{"a bignum", new(big.Int).Lsh(big.NewInt(1), 90), "1237940039285380274899124224"},
		{"a byte string", []byte{1}, ""}, {"inside a map", map[string]any{"a": []byte{1}}, ""}, {"an instant", time.Unix(1, 0), ""},
		{"NaN", math.NaN(), ""}, {"a tag", cbor.Tag{Number: 999, Content: "x"}, ""},
	})
}

// TestEncodeCBOR_TextMode : what a column that reads its value from text gets from each CBOR form.
func TestEncodeCBOR_TextMode(t *testing.T) {
	uuid := []byte{0xa0, 0xee, 0xbc, 0x99, 0x9c, 0x0b, 0x4e, 0xf8, 0xbb, 0x6d, 0x6b, 0xb9, 0xbd, 0x38, 0x0a, 0x11}
	for _, c := range []struct {
		name string
		v    any
		want string
	}{
		{"a text string", "10.0.0.1", "10.0.0.1"}, {"an integer", int64(-7), "-7"}, {"a float", 12.5, "12.5"}, {"a whole float", 3.0, "3"},
		{"tag 4", cbor.Tag{Number: 4, Content: []any{int64(-2), int64(150)}}, "1.50"}, {"a bignum", new(big.Int).Lsh(big.NewInt(1), 70), "1180591620717411303424"},
		{"true", true, "true"}, {"a byte string", []byte{0, 255}, `\x00ff`}, {"tag 37", cbor.Tag{Number: 37, Content: uuid}, "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"},
		{"tag 1", cbor.Tag{Number: 1, Content: 1700000000.125}, "2023-11-14T22:13:20.125Z"}, {"tag 100", cbor.Tag{Number: 100, Content: int64(20000)}, "2024-10-04"},
		{"a map", map[string]any{"b": int64(1), "a": "x"}, `{"a":"x","b":1}`}, {"an array", []any{int64(1), "x"}, `[1,"x"]`},
	} {
		got, err := encodeScalarText(nil, cborOf(t, c.v), nil)
		if err != nil || string(got) != c.want {
			t.Errorf("%s: %q, %v, want %q", c.name, got, err, c.want)
		}
	}
	for name, v := range map[string]any{"a map holding bytes": map[string]any{"a": []byte{1}}, "an unknown tag": cbor.Tag{Number: 999, Content: 1}} {
		if _, err := encodeScalarText(nil, cborOf(t, v), nil); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestEncodeCBOR_ArraysAndComposites(t *testing.T) {
	conn, _ := encTestConn(t, "UTC")
	doc := map[string]any{
		"i":    map[string]any{"a": int64(2), "b": "x"},
		"tags": []any{"q", "r"},
		"n":    cbor.Tag{Number: 4, Content: []any{int64(-1), int64(15)}},
		"t":    time.Date(2026, 10, 18, 12, 30, 0, 0, time.UTC), // tag 1 : an instant, not a string
	}
	got, _, err := ourValue(t, conn, encT(t).ColumnsMap["outer_c"], cborOf(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if want := `("(2,x)","{q,r}",1.50,"2026-10-18 12:30:00+00",)`; got != want {
		t.Errorf("outer_c = %s, want %s", got, want)
	}
	got, _, err = ourValue(t, conn, encT(t).ColumnsMap["byteas"], cborOf(t, []any{[]byte{1}, nil, []byte{}}))
	if err != nil || got != `{"\\x01",NULL,"\\x"}` {
		t.Errorf("byteas = %s, %v", got, err)
	}
	// a text-mode composite whose field is a byte string or an instant
	got, _, err = ourValue(t, conn, encT(t).ColumnsMap["ivc"], cborOf(t, map[string]any{"code": "a,b", "span": "3 days"}))
	if err != nil || got != `("a,b","3 days")` {
		t.Errorf("ivc = %s, %v", got, err)
	}
}
