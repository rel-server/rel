package query

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic/ast"
	"github.com/jackc/pgx/v5/pgxpool"
)

// encWant is what a corpus entry expects of the encoder, against Postgres' own parser.
type encWant int

const (
	// wantAccept : the encoder accepts, and Postgres reads the same text to the same value.
	wantAccept encWant = iota
	// wantReject : Postgres accepts the text but the encoder rejects it. These entries
	// are the documented rejection list of specs/typed-write-loading.md ## Column encoders.
	wantReject
	// wantBad : the encoder and Postgres both reject.
	wantBad
	// wantLate : the encoder passes the value on, and Postgres rejects it when it loads.
	wantLate
	// wantKind : a value of a kind the type never takes ; no Postgres counterpart.
	wantKind
	// wantSame : whatever Postgres does, the encoder does the same : it accepts and reads back
	// to the same value, or it rejects. For edges whose outcome depends on the session zone.
	wantSame
)

type encCase struct {
	js   string // the request value, as JSON text
	pg   string // the text Postgres' input function reads for the comparison ; derived from js when empty
	want encWant
}

func ok(js string, pg ...string) encCase       { return mkCase(js, wantAccept, pg) }
func rejected(js string, pg ...string) encCase { return mkCase(js, wantReject, pg) }
func bad(js string, pg ...string) encCase      { return mkCase(js, wantBad, pg) }
func late(js string, pg ...string) encCase     { return mkCase(js, wantLate, pg) }
func kind(js string) encCase                   { return encCase{js: js, want: wantKind} }

func same(js string, pg ...string) encCase { return mkCase(js, wantSame, pg) }

func mkCase(js string, w encWant, pg []string) encCase {
	c := encCase{js: js, want: w}
	if len(pg) > 0 {
		c.pg = pg[0]
	} else if strings.HasPrefix(js, `"`) {
		_ = json.Unmarshal([]byte(js), &c.pg)
	} else {
		c.pg = js
	}
	return c
}

func encNode(t *testing.T, src string) Node {
	t.Helper()
	root, perr := ast.NewParser(src).Parse()
	if perr != 0 {
		t.Fatalf("test fixture is not JSON: %s", src)
	}
	return sonicNode{&root}
}

var encOIDs sync.Map

func pgTypeOID(t *testing.T, conn *pgxpool.Conn, typeName string) uint32 {
	t.Helper()
	if v, ok := encOIDs.Load(typeName); ok {
		return v.(uint32)
	}
	var oid uint32
	if err := conn.QueryRow(context.Background(), `select '`+typeName+`'::regtype::oid`).Scan(&oid); err != nil {
		t.Fatalf("oid of %s: %v", typeName, err)
	}
	encOIDs.Store(typeName, oid)
	return oid
}

// pgRender has Postgres read field as the binary form of oid and print it back as text.
func pgRender(conn *pgxpool.Conn, oid uint32, field []byte) (string, error) {
	return pgScalar(conn, `select ($1)::text`, [][]byte{field}, []uint32{oid}, []int16{1})
}

// pgParse has Postgres' input function for typeName read text and print the value back as text.
func pgParse(conn *pgxpool.Conn, typeName, text string) (string, error) {
	return pgScalar(conn, `select ($1::text)::`+typeName+`::text`, [][]byte{[]byte(text)}, []uint32{0}, []int16{0})
}

func pgScalar(conn *pgxpool.Conn, sql string, params [][]byte, oids []uint32, formats []int16) (string, error) {
	rr := conn.Conn().PgConn().ExecParams(context.Background(), sql, params, oids, formats, []int16{0})
	res := rr.Read()
	if _, err := rr.Close(); err != nil {
		return "", err
	}
	if res.Err != nil {
		return "", res.Err
	}
	if len(res.Rows) != 1 || res.Rows[0][0] == nil {
		return "", nil
	}
	return string(res.Rows[0][0]), nil
}

// encTestConn is a connection whose session TimeZone is zone, restored when the test ends.
func encTestConn(t *testing.T, zone string) (*pgxpool.Conn, *encodeContext) {
	t.Helper()
	conn, err := testDb.Pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `reset time zone`)
		conn.Release()
	})
	if _, err := conn.Exec(context.Background(), `set time zone '`+zone+`'`); err != nil {
		t.Fatalf("set time zone: %v", err)
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatalf("load %s: %v", zone, err)
	}
	return conn, &encodeContext{Location: loc}
}

// runEncCases checks every entry against the encoder and Postgres, once per source : the JSON text as it is, and the
// same value as CBOR (numbers that are not whole become exact tag 4 decimals), which must give the same answer.
func runEncCases(t *testing.T, conn *pgxpool.Conn, ec *encodeContext, typeName string, enc fieldEncoder, cases []encCase) {
	t.Helper()
	oid := pgTypeOID(t, conn, typeName)
	for _, c := range cases {
		for _, src := range encSources {
			node, ok := src.node(t, c.js, typeName)
			if !ok {
				continue
			}
			checkEncCase(t, conn, ec, typeName, oid, enc, c, node, src.name)
		}
	}
}

func checkEncCase(t *testing.T, conn *pgxpool.Conn, ec *encodeContext, typeName string, oid uint32, enc fieldEncoder, c encCase, node Node, src string) {
	t.Helper()
	field, encErr := enc(nil, node, ec)
	if encErr == nil && field == nil {
		field = []byte{} // an empty value, not a NULL
	}

	if c.want == wantKind {
		if encErr == nil {
			t.Errorf("%s %s [%s]: accepted a value of the wrong kind", typeName, c.js, src)
		}
		return
	}
	pgVal, pgErr := pgParse(conn, typeName, c.pg)

	switch c.want {
	case wantAccept:
		if encErr != nil {
			t.Errorf("%s %s [%s]: rejected, want accepted (Postgres reads %q as %q): %v", typeName, c.js, src, c.pg, pgVal, encErr)
			return
		}
		if pgErr != nil {
			t.Errorf("%s %s [%s]: corpus error, Postgres rejects %q: %v", typeName, c.js, src, c.pg, pgErr)
			return
		}
		got, err := pgRender(conn, oid, field)
		if err != nil {
			t.Errorf("%s %s [%s]: Postgres rejects the encoded bytes % x: %v", typeName, c.js, src, field, err)
		} else if got != pgVal {
			t.Errorf("%s %s [%s]: encoded value reads back as %q, Postgres reads the text as %q", typeName, c.js, src, got, pgVal)
		}
	case wantSame:
		switch {
		case pgErr != nil && encErr == nil:
			t.Errorf("%s %s [%s]: accepted what Postgres rejects (%v)", typeName, c.js, src, pgErr)
		case pgErr == nil && encErr != nil:
			t.Errorf("%s %s [%s]: rejected, Postgres reads %q as %q: %v", typeName, c.js, src, c.pg, pgVal, encErr)
		case pgErr == nil:
			if got, err := pgRender(conn, oid, field); err != nil || got != pgVal {
				t.Errorf("%s %s [%s]: encoded value reads back as %q (%v), Postgres reads the text as %q", typeName, c.js, src, got, err, pgVal)
			}
		}
	case wantReject:
		if encErr == nil {
			t.Errorf("%s %s [%s]: accepted, but this is on the documented rejection list", typeName, c.js, src)
		}
		if pgErr != nil {
			t.Errorf("%s %s [%s]: corpus error, Postgres rejects %q too (use bad): %v", typeName, c.js, src, c.pg, pgErr)
		}
	case wantBad:
		if encErr == nil {
			t.Errorf("%s %s [%s]: accepted what Postgres rejects (%v)", typeName, c.js, src, pgErr)
		}
		if pgErr == nil {
			t.Errorf("%s %s [%s]: corpus error, Postgres accepts %q as %q (use rejected)", typeName, c.js, src, c.pg, pgVal)
		}
	case wantLate:
		if encErr != nil {
			t.Errorf("%s %s [%s]: rejected early, want passed to Postgres: %v", typeName, c.js, src, encErr)
			return
		}
		if _, err := pgRender(conn, oid, field); err == nil {
			t.Errorf("%s %s [%s]: Postgres accepts the bytes, so this is not a late rejection", typeName, c.js, src)
		}
	}
}

func TestEncode_Integers(t *testing.T) {
	conn, ec := encTestConn(t, "UTC")
	runEncCases(t, conn, ec, "int2", encodeInt(16), []encCase{
		ok(`0`), ok(`32767`), ok(`-32768`), ok(`"12"`), ok(`"-0"`), ok(`"+7"`),
		bad(`32768`), bad(`-32769`), bad(`"99999999999"`),
	})
	runEncCases(t, conn, ec, "int4", encodeInt(32), []encCase{
		ok(`0`), ok(`42`), ok(`-42`), ok(`2147483647`), ok(`-2147483648`), ok(`"42"`), ok(`"+42"`), ok(`"-0"`), ok(`-0`),
		bad(`2147483648`), bad(`-2147483649`),
		bad(`1.5`), bad(`1.0`, `1.0`), bad(`1e3`), bad(`"abc"`), bad(`""`), bad(`"4 2"`), bad(`"--1"`), bad(`"1.5"`),
		rejected(`" 42 "`), rejected(`"0x1F"`, `0x1F`), rejected(`"1_000"`), rejected(`"0b101"`), rejected(`"0o17"`),
		kind(`true`), kind(`{"a":1}`), kind(`[1]`), kind(`null`),
	})
	runEncCases(t, conn, ec, "int8", encodeInt(64), []encCase{
		ok(`0`), ok(`9223372036854775807`), ok(`-9223372036854775808`), ok(`"9223372036854775807"`), ok(`9007199254740993`),
		bad(`9223372036854775808`), bad(`-9223372036854775809`), bad(`1.5`),
	})
}

func TestEncode_Floats(t *testing.T) {
	conn, ec := encTestConn(t, "UTC")
	runEncCases(t, conn, ec, "float8", encodeFloat(64), []encCase{
		ok(`0`), ok(`-0`), ok(`1.5`), ok(`-1.5e10`), ok(`1e308`), ok(`4.9e-324`), ok(`2.2250738585072014e-308`), ok(`0.1`),
		ok(`123456789.123456789`), ok(`"1.5"`), ok(`"1e3"`), ok(`".5"`), ok(`"5."`), ok(`"+5"`), ok(`"NaN"`), ok(`"nan"`),
		ok(`"Infinity"`), ok(`"-Infinity"`), ok(`"inf"`), ok(`"-inf"`), ok(`"+inf"`),
		bad(`1e309`), bad(`"abc"`), bad(`""`), bad(`"1e"`), bad(`"."`),
		rejected(`"0x1p-2"`), bad(`"1_0"`), rejected(`" 1.5 "`),
		kind(`true`), kind(`[1]`),
	})
	runEncCases(t, conn, ec, "float4", encodeFloat(32), []encCase{
		ok(`0`), ok(`1.5`), ok(`3.4028235e38`), ok(`1.17549435e-38`), ok(`0.1`), ok(`16777217`), ok(`"NaN"`), ok(`"-Infinity"`),
		bad(`3.5e38`), bad(`"abc"`),
	})
}

func TestEncode_Bool(t *testing.T) {
	conn, ec := encTestConn(t, "UTC")
	runEncCases(t, conn, ec, "bool", encodeBool, []encCase{
		ok(`true`, `true`), ok(`false`, `false`), ok(`1`), ok(`0`),
		ok(`"t"`), ok(`"f"`), ok(`"true"`), ok(`"FALSE"`), ok(`"tr"`), ok(`"fal"`), ok(`"yes"`), ok(`"y"`), ok(`"no"`), ok(`"n"`),
		ok(`"on"`), ok(`"off"`), ok(`"of"`), ok(`"1"`), ok(`"0"`), ok(`" true "`),
		bad(`2`), bad(`-1`), bad(`1.0`), bad(`"o"`), bad(`"nope"`), bad(`""`), bad(`"truee"`), bad(`"11"`),
		kind(`{"a":1}`), kind(`[true]`),
	})
}

func TestEncode_Text(t *testing.T) {
	conn, ec := encTestConn(t, "UTC")
	for _, typ := range []string{"text", "varchar", "bpchar", "name"} {
		runEncCases(t, conn, ec, typ, encodeText, []encCase{
			ok(`"abc"`), ok(`""`), ok(`"héllo ✓ 日本語 😀"`), ok(`"with \"quotes\" and \\ backslash\n newline"`),
			bad(`"a\u0000b"`, "a\x00b"),
			kind(`42`), kind(`true`), kind(`{"a":1}`), kind(`["a"]`),
		})
	}
}

func TestEncode_UUID(t *testing.T) {
	conn, ec := encTestConn(t, "UTC")
	runEncCases(t, conn, ec, "uuid", encodeUUID, []encCase{
		ok(`"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"`), ok(`"A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11"`),
		ok(`"a0eebc999c0b4ef8bb6d6bb9bd380a11"`), ok(`"{a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11}"`),
		ok(`"{a0eebc999c0b4ef8bb6d6bb9bd380a11}"`), ok(`"00000000-0000-0000-0000-000000000000"`),
		bad(`"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a1"`), bad(`"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11a"`), bad(`"g0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"`),
		bad(`""`),
		rejected(`"a0ee-bc99-9c0b-4ef8-bb6d-6bb9-bd38-0a11"`), rejected(`"a0eebc99-9c0b-4ef8-bb6d6bb9bd380a11"`),
		kind(`42`), kind(`true`),
	})
}

func TestEncode_Bytea(t *testing.T) {
	conn, ec := encTestConn(t, "UTC")
	runEncCases(t, conn, ec, "bytea", encodeBytea, []encCase{
		ok(`"\\x00ff"`), ok(`"\\x"`), ok(`"\\xABcd"`), ok(`"\\xdeadbeef"`),
		bad(`"\\x0"`), bad(`"\\xzz"`),
		rejected(`"abc"`), rejected(`"\\x00 ff"`), rejected(`"\\000"`, `\000`),
		kind(`42`), kind(`[1,2]`),
	})
}

func TestEncode_Numeric(t *testing.T) {
	conn, ec := encTestConn(t, "UTC")
	runEncCases(t, conn, ec, "numeric", encodeNumeric, []encCase{
		ok(`0`), ok(`0.00`), ok(`-0`), ok(`-0.0`), ok(`1`), ok(`-1`), ok(`1.50`), ok(`1.5`), ok(`10`), ok(`100`), ok(`9999`), ok(`10000`),
		ok(`10001`), ok(`99999999`), ok(`100000000`), ok(`9999.9999`), ok(`0.1`), ok(`0.0001`), ok(`0.00012`), ok(`0.000012`),
		ok(`123456789012345678901234567890.123456789`), ok(`-123456789012345678901234567890.123456789`),
		ok(`1e3`), ok(`1E3`), ok(`1.5e-3`), ok(`12345678.9e2`), ok(`1e-5`), ok(`1e1000`), ok(`1.00e2`), ok(`0e5`), ok(`0.0e-3`),
		ok(`1000000000000000000000000000000000000000000000000001`),
		ok(`"12.50"`), ok(`"1e3"`), ok(`"+5"`), ok(`".5"`), ok(`"5."`), ok(`"-.5e1"`), ok(`"NaN"`), ok(`"nan"`),
		ok(`"Infinity"`), ok(`"-Infinity"`), ok(`"inf"`), ok(`"-inf"`),
		bad(`""`, ``), bad(`"abc"`), bad(`"1e"`), bad(`"--1"`), bad(`"1.2.3"`), bad(`"e5"`), bad(`"."`), bad(`"+"`), bad(`1e200000`),
		rejected(`" 5 "`), rejected(`"0x1F"`), rejected(`"1_000"`),
		kind(`true`), kind(`{"a":1}`), kind(`[1]`),
	})
}

func TestEncode_Date(t *testing.T) {
	conn, ec := encTestConn(t, "UTC")
	runEncCases(t, conn, ec, "date", encodeDate, []encCase{
		ok(`"2026-10-18"`), ok(`"0001-01-01"`), ok(`"1970-01-01"`), ok(`"2000-01-01"`), ok(`"1999-12-31"`), ok(`"2024-02-29"`),
		ok(`"1900-02-28"`), ok(`"2000-02-29"`), ok(`"0044-03-15 BC"`), ok(`"4714-11-24 BC"`), ok(`"5874897-12-31"`), ok(`"10000-01-01"`),
		ok(`"infinity"`), ok(`"-infinity"`), ok(`"Infinity"`),
		bad(`"4714-11-23 BC"`), bad(`"5874898-01-01"`), bad(`"2026-02-29"`), bad(`"1900-02-29"`), bad(`"2026-13-01"`), bad(`"2026-00-10"`),
		bad(`"2026-10-32"`), bad(`"0000-01-01"`), bad(`""`), bad(`"2026-10"`), bad(`"abc"`),
		rejected(`"10/11/2026"`), rejected(`"2026-10-18T00:00:00"`), rejected(`"today"`), rejected(`"20261018"`), rejected(`"2026-1-5"`),
		rejected(`" 2026-10-18"`),
		kind(`20261018`), kind(`true`), kind(`{"a":1}`),
	})
}

var timestampCases = []encCase{
	ok(`"2026-10-18 14:30:00"`), ok(`"2026-10-18T14:30:00"`), ok(`"2026-10-18T14:30:00.5"`), ok(`"2026-10-18T14:30:00.123456"`),
	ok(`"2026-10-18T14:30:00.000001"`), ok(`"2026-10-18"`), ok(`"2026-10-18 14:30"`), ok(`"2026-10-18T00:00:00"`),
	ok(`"2026-10-18T24:00:00"`), ok(`"2026-10-18T23:59:59.999999"`), ok(`"2000-01-01 00:00:00"`), ok(`"1999-12-31 23:59:59.999999"`),
	ok(`"1970-01-01 00:00:00"`), ok(`"0001-01-01 00:00:00"`), ok(`"0044-03-15 00:00:00 BC"`), ok(`"0044-03-15T10:20:30 BC"`),
	ok(`"4714-11-24 00:00:00 BC"`), ok(`"294276-12-31 23:59:59.999999"`), ok(`"10000-01-01 00:00:00"`), ok(`"2024-02-29 12:00:00"`),
	ok(`"infinity"`), ok(`"-infinity"`),
	bad(`"4714-11-23 23:59:59.999999 BC"`), bad(`"294277-01-01 00:00:00"`), bad(`"2026-02-29 00:00:00"`), bad(`"2026-10-18 25:00:00"`),
	bad(`"2026-10-18 14:60:00"`), bad(`"2026-10-18 24:00:01"`), bad(`"2026-10-18 24:01:00"`), bad(`"0000-01-01 00:00:00"`), bad(`""`),
	bad(`"abc"`), bad(`"2026-10-18T"`), bad(`"2026-10-18 14"`),
	rejected(`"2026-10-18T14:30:00.1234567"`), rejected(`"2026-10-18T23:59:60"`), rejected(`"now"`), rejected(`"today"`), rejected(`"epoch"`),
	rejected(`"10/11/2026 14:30"`), rejected(`"20261018T143000"`), rejected(`"2026-10-18  14:30:00"`), rejected(`" 2026-10-18 14:30:00"`),
	rejected(`"Oct 18 2026"`), rejected(`"2026-10-18T14:30:00."`),
	kind(`1760000000`), kind(`true`), kind(`{"a":1}`),
}

func TestEncode_Timestamp(t *testing.T) {
	conn, ec := encTestConn(t, "UTC")
	cases := append([]encCase{}, timestampCases...)
	// A timestamp has no zone : a suffix is read and ignored.
	cases = append(cases,
		ok(`"2026-10-18T14:30:00+02:00"`), ok(`"2026-10-18T14:30:00Z"`), ok(`"2026-10-18T14:30:00-05:30"`), ok(`"2026-10-18 14:30:00+02"`),
		ok(`"2026-10-18 14:30:00 +02:00"`), ok(`"2026-10-18T14:30:00+0200"`),
		ok(`"0044-03-15T00:00:00+00:00 BC"`),
	)
	runEncCases(t, conn, ec, "timestamp", encodeTimestamp, cases)
}

var timestamptzZoneCases = []encCase{
	ok(`"2026-10-18T14:30:00Z"`), ok(`"2026-10-18T14:30:00+00:00"`), ok(`"2026-10-18T14:30:00+02:00"`), ok(`"2026-10-18T14:30:00-05:30"`),
	ok(`"2026-10-18 14:30:00+02"`), ok(`"2026-10-18 14:30:00 +02:00"`), ok(`"2026-10-18T14:30:00+0530"`), ok(`"2026-10-18T14:30:00.123456+02:00"`),
	ok(`"2026-10-18T24:00:00+02:00"`), ok(`"1999-12-31T23:59:59.999999Z"`),
	ok(`"0044-03-15T00:00:00+00:00 BC"`), ok(`"4714-11-24T00:00:00+00:00 BC"`), ok(`"294276-12-31T23:59:59.999999+00:00"`),
	ok(`"infinity"`), ok(`"-infinity"`),
	bad(`"4714-11-23T23:59:59.999999+00:00 BC"`), bad(`"294277-01-01T00:00:00+00:00"`), bad(`"2026-10-18T14:30:00+25:00"`),
	same(`"4714-11-24T00:00:00+05:00 BC"`), same(`"4714-11-24T00:00:00-05:00 BC"`), same(`"294276-12-31T23:59:59.999999-05:00"`), same(`"294276-12-31T23:59:59.999999+05:00"`),
	rejected(`"2026-10-18T14:30:00 Europe/Paris"`), rejected(`"2026-10-18T14:30:00 PST"`), rejected(`"2026-10-18T14:30:00+02:00:30"`),
}

var timestamptzWallClockCases = []encCase{
	ok(`"2026-10-18 14:30:00"`), ok(`"2026-10-18T14:30:00"`), ok(`"2026-10-18"`), ok(`"2026-01-15 08:00:00"`), ok(`"2026-07-15 08:00:00"`),
	// daylight-saving gaps and overlaps of the zones below
	ok(`"2026-03-29 02:30:00"`), ok(`"2026-10-25 02:30:00"`), ok(`"2026-03-08 02:30:00"`), ok(`"2026-11-01 01:30:00"`),
	ok(`"2026-09-27 02:30:00"`), ok(`"2026-04-05 02:30:00"`), ok(`"2026-03-29 01:59:59"`), ok(`"2026-10-25 03:00:00"`),
	ok(`"1999-12-31 23:59:59.999999"`), ok(`"2000-01-01 00:00:00"`), ok(`"1970-01-01 00:00:00"`), ok(`"1950-06-15 12:00:00"`),
}

func TestEncode_Timestamptz(t *testing.T) {
	for _, zone := range []string{"UTC", "Europe/Paris", "America/New_York", "Asia/Kolkata", "Pacific/Auckland", "Australia/Lord_Howe"} {
		t.Run(zone, func(t *testing.T) {
			conn, ec := encTestConn(t, zone)
			var cases []encCase
			for _, c := range timestampCases {
				// range edges depend on the zone's offset : checked below against Postgres itself
				if !strings.Contains(c.js, "4714-11-2") && !strings.Contains(c.js, "294276-12-31") && !strings.Contains(c.js, "294277-01-01") {
					cases = append(cases, c)
				}
			}
			for _, js := range []string{`"4714-11-24 00:00:00 BC"`, `"4714-11-23 23:59:59.999999 BC"`, `"4714-11-24 05:00:00 BC"`,
				`"294276-12-31 23:59:59.999999"`, `"294276-12-31 10:00:00"`, `"294277-01-01 00:00:00"`, `"294277-01-01 10:00:00"`} {
				cases = append(cases, same(js))
			}
			cases = append(cases, timestamptzZoneCases...)
			cases = append(cases, timestamptzWallClockCases...)
			runEncCases(t, conn, ec, "timestamptz", encodeTimestamptz, cases)
		})
	}
}

func TestEncode_JSON(t *testing.T) {
	conn, ec := encTestConn(t, "UTC")
	docs := []encCase{
		ok(`{"a":1}`), ok(`[1,2,3]`), ok(`"str"`), ok(`42`), ok(`1.50`), ok(`true`), ok(`{"a":{"b":[1,{"c":null}]}}`),
		ok(`{ "spaced" : [ 1 , 2 ] }`), ok(`12345678901234567890123456789`), ok(`1e400`), ok(`"héllo ✓"`), ok(`{"a":1,"a":2}`),
		ok(`[]`), ok(`{}`), ok(`""`),
	}
	for i := range docs {
		docs[i].pg = docs[i].js // the JSON text itself, not a string's content
	}
	runEncCases(t, conn, ec, "jsonb", encodeJSONB, append(docs, late(`"a\u0000b"`, `"a\u0000b"`)))
	// json keeps its text as written, which sonic re-serializes without the whitespace around a key
	docs = append(docs[:7:7], docs[8:]...)
	runEncCases(t, conn, ec, "json", encodeJSON, docs)
}

// zoneTransitions lists the instants loc changes offset between from and to.
func zoneTransitions(loc *time.Location, from, to time.Time) []time.Time {
	var out []time.Time
	for t := from; t.Before(to); {
		_, end := t.In(loc).ZoneBounds()
		if end.IsZero() || !end.Before(to) {
			break
		}
		if !end.After(t) { // far in the future ZoneBounds reports artificial bounds : step past them
			t = t.AddDate(0, 0, 1)
			continue
		}
		out = append(out, end)
		t = end
	}
	return out
}

// bulkTimestamptzCheck encodes every text with the encoder, and has Postgres read both the texts
// and the encoded instants in two queries, which must agree.
// tzdataDifferences lists transitions where the time zone database on a development machine
// and the one in the Postgres image disagree : a wall-clock reading near one gets a different
// instant from Go and from Postgres, whatever the encoder does. The test tolerates mismatches
// only there, and counts them so a wider disagreement still fails.
var tzdataDifferences = map[string]string{
	"Europe/Dublin":     "1925-09-2",
	"Asia/Tehran":       "1979-05-2",
	"Africa/Casablanca": "2026-09-2",
}

func bulkTimestamptzCheck(t *testing.T, conn *pgxpool.Conn, ec *encodeContext, zone string, texts []string) {
	t.Helper()
	ctx := context.Background()
	micros := make([]int64, len(texts))
	for i, s := range texts {
		field, err := encodeTimestamptz(nil, encNode(t, `"`+s+`"`), ec)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		micros[i] = int64(uint64(field[0])<<56 | uint64(field[1])<<48 | uint64(field[2])<<40 | uint64(field[3])<<32 |
			uint64(field[4])<<24 | uint64(field[5])<<16 | uint64(field[6])<<8 | uint64(field[7]))
	}
	readQuery := func(sql string, arg any) []string {
		rows, err := conn.Query(ctx, sql, arg)
		if err != nil {
			t.Fatalf("%v", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
			out = append(out, v)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	want := readQuery(`select (t::timestamptz)::text from unnest($1::text[]) with ordinality as u(t, i) order by i`, texts)
	got := readQuery(`select (timestamptz '2000-01-01 00:00:00+00' + (m / 3600000000) * interval '1 hour' + (m % 3600000000) * interval '1 microsecond')::text from unnest($1::bigint[]) with ordinality as u(m, i) order by i`, micros)
	if len(want) != len(texts) || len(got) != len(texts) {
		t.Fatalf("%d texts, %d read by Postgres, %d encoded", len(texts), len(want), len(got))
	}
	bad, tolerated := 0, 0
	for i := range texts {
		if want[i] == got[i] {
			continue
		}
		if prefix := tzdataDifferences[zone]; prefix != "" && strings.HasPrefix(texts[i], prefix) {
			tolerated++
			continue
		}
		if bad++; bad <= 10 {
			t.Errorf("%s: encoded as %s, Postgres reads %s", texts[i], got[i], want[i])
		}
	}
	if bad > 10 {
		t.Errorf("… %d mismatches in all", bad)
	}
	if tolerated > 300 {
		t.Errorf("%d mismatches around the known time zone database difference, more than one or two transitions explain", tolerated)
	}
	if tolerated > 0 {
		t.Logf("%d readings differ because the time zone databases do", tolerated)
	}
}

func TestEncode_TimestamptzAroundEveryClockChange(t *testing.T) {
	zones := []string{
		"UTC", "Europe/Paris", "Europe/London", "Europe/Dublin", "Europe/Moscow", "America/New_York", "America/Los_Angeles",
		"America/St_Johns", "America/Sao_Paulo", "Asia/Kolkata", "Asia/Kathmandu", "Asia/Tehran", "Australia/Sydney",
		"Australia/Lord_Howe", "Pacific/Auckland", "Pacific/Apia", "Pacific/Kiritimati", "Africa/Casablanca", "Antarctica/Troll",
	}
	from, to := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2060, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, zone := range zones {
		t.Run(zone, func(t *testing.T) {
			conn, ec := encTestConn(t, zone)
			var texts []string
			for _, tr := range zoneTransitions(ec.Location, from, to) {
				// every quarter hour of wall-clock time within 30 hours of the change, on either side of it
				_, before := tr.Add(-time.Second).In(ec.Location).Zone()
				_, after := tr.In(ec.Location).Zone()
				for _, off := range []int{before, after} {
					base := tr.UTC().Add(time.Duration(off) * time.Second)
					for d := -30 * 4; d <= 30*4; d++ {
						w := base.Add(time.Duration(d) * 15 * time.Minute)
						texts = append(texts, w.Format("2006-01-02 15:04:05"))
					}
				}
			}
			if len(texts) == 0 {
				for h := 0; h < 24*400; h += 7 {
					texts = append(texts, from.Add(time.Duration(h)*time.Hour).Format("2006-01-02 15:04:05"))
				}
			}
			bulkTimestamptzCheck(t, conn, ec, zone, texts)
			t.Logf("%d wall-clock times", len(texts))
		})
	}
}

// bulkRead runs a query taking one array argument and returns its single text column.
func bulkRead(t *testing.T, conn *pgxpool.Conn, sql string, arg any) []string {
	t.Helper()
	rows, err := conn.Query(context.Background(), sql, arg)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func be64(b []byte) int64 {
	return int64(uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 | uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7]))
}

// isoYear prints year y of the proleptic calendar the way rel's output does, 4 digits at least, BC after year 0.
func isoYear(y int) (string, bool) {
	if y <= 0 {
		return fmt.Sprintf("%04d", 1-y), true
	}
	return fmt.Sprintf("%04d", y), false
}

func TestEncode_DateAcrossTheWholeRange(t *testing.T) {
	conn, _ := encTestConn(t, "UTC")
	var texts []string
	add := func(y, m, d int) {
		ys, bc := isoYear(y)
		s := fmt.Sprintf("%s-%02d-%02d", ys, m, d)
		if bc {
			s += " BC"
		}
		texts = append(texts, s)
	}
	for y := -4712; y <= 9999; y++ {
		for _, md := range [][2]int{{1, 1}, {2, 28}, {3, 1}, {12, 31}} {
			add(y, md[0], md[1])
		}
		if y%4 == 0 && (y%100 != 0 || y%400 == 0) {
			add(y, 2, 29)
		}
	}
	for y := 10000; y < 5874897; y += 4801 {
		add(y, 6, 15)
	}
	add(-4713, 11, 24)
	add(5874897, 12, 31)

	var days []int32
	for _, s := range texts {
		field, err := encodeDate(nil, encNode(t, `"`+s+`"`), nil)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		days = append(days, int32(be64(append(make([]byte, 4), field...))))
	}
	want := bulkRead(t, conn, `select (t::date)::text from unnest($1::text[]) with ordinality as u(t, i) order by i`, texts)
	got := bulkRead(t, conn, `select (date '2000-01-01' + d)::text from unnest($1::int[]) with ordinality as u(d, i) order by i`, days)
	for i := range texts {
		if want[i] != got[i] {
			t.Fatalf("%s: encoded as %s, Postgres reads %s", texts[i], got[i], want[i])
		}
	}
	t.Logf("%d dates", len(texts))
}

func TestEncode_TimestampAcrossTheWholeRange(t *testing.T) {
	conn, _ := encTestConn(t, "UTC")
	rnd := rand.New(rand.NewSource(1))
	var texts []string
	for i := 0; i < 60000; i++ {
		y := rnd.Intn(294276+4712) - 4712
		if i%3 == 0 {
			y = rnd.Intn(300) + 1850 // dense around the present
		}
		ys, bc := isoYear(y)
		m, d := rnd.Intn(12)+1, rnd.Intn(28)+1
		s := fmt.Sprintf("%s-%02d-%02d %02d:%02d:%02d", ys, m, d, rnd.Intn(24), rnd.Intn(60), rnd.Intn(60))
		if i%2 == 0 {
			s += fmt.Sprintf(".%06d", rnd.Intn(1000000))
		}
		if bc {
			s += " BC"
		}
		texts = append(texts, s)
	}
	var micros []int64
	for _, s := range texts {
		field, err := encodeTimestamp(nil, encNode(t, `"`+s+`"`), nil)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		micros = append(micros, be64(field))
	}
	want := bulkRead(t, conn, `select (t::timestamp)::text from unnest($1::text[]) with ordinality as u(t, i) order by i`, texts)
	got := bulkRead(t, conn, `select (timestamp '2000-01-01 00:00:00' + (m / 86400000000) * interval '1 day' + (m % 86400000000) * interval '1 microsecond')::text from unnest($1::bigint[]) with ordinality as u(m, i) order by i`, micros)
	for i := range texts {
		if want[i] != got[i] {
			t.Fatalf("%s: encoded as %s, Postgres reads %s", texts[i], got[i], want[i])
		}
	}
}

func TestEncode_NumericRandomDigits(t *testing.T) {
	conn, ec := encTestConn(t, "UTC")
	rnd := rand.New(rand.NewSource(2))
	digits := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte('0' + rnd.Intn(10))
			if rnd.Intn(6) == 0 {
				b[i] = '0' // runs of zeros exercise the group alignment
			}
		}
		return string(b)
	}
	var texts []string
	for i := 0; i < 20000; i++ {
		s := ""
		if rnd.Intn(3) == 0 {
			s = "-"
		}
		s += digits(rnd.Intn(30))
		if rnd.Intn(3) != 0 {
			s += "." + digits(rnd.Intn(30))
		}
		if s == "" || s == "-" || strings.HasSuffix(s, ".") && len(strings.Trim(s, "-.")) == 0 {
			s += "0"
		}
		if rnd.Intn(4) == 0 {
			s += fmt.Sprintf("e%d", rnd.Intn(80)-40)
		}
		texts = append(texts, s)
	}
	var fields [][]byte
	for _, s := range texts {
		f, err := appendNumeric(nil, s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		fields = append(fields, f)
	}
	oid := pgTypeOID(t, conn, "numeric")
	_ = ec
	want := bulkRead(t, conn, `select (t::numeric)::text from unnest($1::text[]) with ordinality as u(t, i) order by i`, texts)
	for i, f := range fields {
		got, err := pgRender(conn, oid, f)
		if err != nil || got != want[i] {
			t.Fatalf("%s: encoded as %q (%v), Postgres reads %q", texts[i], got, err, want[i])
		}
	}
}
