package query

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rel-server/rel/pg"
)

// colCase is one value for a column of enc_t, and what the encoder must do with it
// compared with jsonb_populate_record, the oracle : the write path of today.
type colCase struct {
	js   string
	want encWant // wantAccept, wantBad (both reject), wantReject (oracle accepts, documented rejection), wantKind
}

func encT(t *testing.T) *pg.Relation {
	t.Helper()
	r := testDb.ResolveRelation("public", "enc_t")
	if r == nil {
		t.Fatal("fixture enc_t is missing")
	}
	return r
}

// oracleValue is what jsonb_populate_record makes of value for column col, printed as text.
func oracleValue(conn *pgxpool.Conn, col, value string) (string, error) {
	return pgScalar(conn, `select (jsonb_populate_record(null::enc_t, jsonb_build_object('`+col+`', $1::jsonb))).`+col+`::text`,
		[][]byte{[]byte(value)}, []uint32{0}, []int16{0})
}

// ourValue encodes value for the column with the mode the encoders pick, loads it the way a temp
// table column would (binary mode keeps the declared type, text mode loads text and casts it to the
// type without modifiers), assigns it to a column of the declared type, and prints it as text.
func ourValue(t *testing.T, conn *pgxpool.Conn, col *pg.Column, value string) (text string, binary bool, err error) {
	t.Helper()
	ctx := context.Background()
	enc, binary := columnEncoder(col.Type)
	node := encNode(t, value)
	field, err := enc(nil, node, &encodeContext{})
	if err != nil {
		return "", binary, err
	}
	if field == nil {
		field = []byte{}
	}
	if _, err := conn.Exec(ctx, `create temp table if not exists enc_probe (v `+col.DeclaredType+`)`); err != nil {
		t.Fatalf("probe table for %s: %v", col.DeclaredType, err)
	}
	defer func() { _, _ = conn.Exec(ctx, `drop table if exists enc_probe`) }()
	if binary {
		_, err = pgScalar(conn, `insert into enc_probe values ($1)`, [][]byte{field}, []uint32{uint32(col.Type.PgOid)}, []int16{1})
	} else {
		_, err = pgScalar(conn, `insert into enc_probe select ($1::text)::`+col.Type.PgIdentifier.EscapedString(), [][]byte{field}, []uint32{0}, []int16{0})
	}
	if err != nil {
		return "", binary, err
	}
	text, err = pgScalar(conn, `select v::text from enc_probe`, nil, nil, nil)
	return text, binary, err
}

func runColCases(t *testing.T, name string, wantBinary bool, cases []colCase) {
	t.Helper()
	conn, _ := encTestConn(t, "UTC")
	col := encT(t).ColumnsMap[name]
	if col == nil {
		t.Fatalf("enc_t has no column %s", name)
	}
	for _, c := range cases {
		ours, binary, ourErr := ourValue(t, conn, col, c.js)
		if binary != wantBinary {
			t.Errorf("%s: binary mode = %v, want %v", name, binary, wantBinary)
			return
		}
		oracle, oracleErr := oracleValue(conn, name, c.js)
		switch c.want {
		case wantAccept:
			switch {
			case ourErr != nil:
				t.Errorf("%s %s: rejected: %v (oracle reads %q, err %v)", name, c.js, ourErr, oracle, oracleErr)
			case oracleErr != nil:
				t.Errorf("%s %s: corpus error, the oracle rejects: %v", name, c.js, oracleErr)
			case ours != oracle:
				t.Errorf("%s %s: reads back as %q, the oracle gives %q", name, c.js, ours, oracle)
			}
		case wantBad:
			if ourErr == nil {
				t.Errorf("%s %s: accepted what the oracle rejects (%v), reads back as %q", name, c.js, oracleErr, ours)
			}
			if oracleErr == nil {
				t.Errorf("%s %s: corpus error, the oracle accepts it as %q (use rejected)", name, c.js, oracle)
			}
		case wantReject:
			if ourErr == nil {
				t.Errorf("%s %s: accepted, but this is on the documented rejection list", name, c.js)
			}
			if oracleErr != nil {
				t.Errorf("%s %s: corpus error, the oracle rejects it too (use bad): %v", name, c.js, oracleErr)
			}
		case wantKind:
			if ourErr == nil {
				t.Errorf("%s %s: accepted a value of the wrong kind, reads back as %q", name, c.js, ours)
			}
		}
	}
}

func okc(js string) colCase       { return colCase{js, wantAccept} }
func badc(js string) colCase      { return colCase{js, wantBad} }
func rejectedc(js string) colCase { return colCase{js, wantReject} }
func kindc(js string) colCase     { return colCase{js, wantKind} }

func TestEncode_Arrays(t *testing.T) {
	runColCases(t, "i4s", true, []colCase{
		okc(`[]`), okc(`[1]`), okc(`[1,2,3]`), okc(`[1,null,3]`), okc(`[null]`), okc(`["1","2"]`), okc(`[1,"2",3]`),
		okc(`[[1,2],[3,4]]`), okc(`[[1],[2],[3]]`), okc(`[[[1,2],[3,4]],[[5,6],[7,8]]]`), okc(`[[1,null],[null,4]]`),
		okc(`[[]]`), okc(`[[],[]]`),
		badc(`[[1,2],[3]]`), badc(`[[1,2],3]`), badc(`[1,[2]]`), badc(`[1.5]`), badc(`[2147483648]`), badc(`["x"]`),
		rejectedc(`"{1,2}"`),
		kindc(`1`), kindc(`{"a":1}`), kindc(`true`),
	})
	runColCases(t, "txts", true, []colCase{
		okc(`["a","b"]`), okc(`[]`), okc(`["", "x y", "with \"quote\"", "{braces}", "a,b", "NULL", "null"]`), okc(`[null,"a"]`),
		okc(`[["a","b"],["c","d"]]`), okc(`["héllo ✓"]`),
		rejectedc(`[1]`), rejectedc(`[true]`), badc(`[["a"],["b","c"]]`),
	})
	runColCases(t, "nums", true, []colCase{
		okc(`[1.234, 2]`), okc(`["NaN", 1.005]`), okc(`[123456.789]`), okc(`[null]`), okc(`[]`),
		badc(`[1000000]`), badc(`["x"]`),
	})
	runColCases(t, "uuids", true, []colCase{
		okc(`["a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"]`), okc(`["A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11", null]`), okc(`[]`), badc(`["zz"]`),
	})
	runColCases(t, "tss", true, []colCase{
		okc(`["2026-10-18T14:30:00Z"]`), okc(`["2026-10-18 14:30:00+02:00", null, "infinity"]`), okc(`[]`),
		badc(`["not a date"]`),
	})
	runColCases(t, "bools", true, []colCase{
		okc(`[true,false,null]`), okc(`["yes","off"]`), okc(`[1,0]`), badc(`[2]`), badc(`["maybe"]`),
	})
	runColCases(t, "byteas", true, []colCase{
		okc(`["\\x00ff", "\\x"]`), okc(`[null]`), badc(`["\\xzz"]`),
	})
	// json and jsonb elements are values, never dimensions
	runColCases(t, "jsonbs", true, []colCase{
		okc(`[{"a":1}, [1,2], "s", 3, true]`), okc(`[[1,2],[3,4]]`), okc(`[]`), okc(`[[]]`),
	})
	runColCases(t, "jsons", true, []colCase{
		okc(`[[1,2],[3,4]]`), okc(`[]`),
	})
}

func TestEncode_Composites(t *testing.T) {
	runColCases(t, "inner_c", true, []colCase{
		okc(`{"a":1,"b":"x"}`), okc(`{"a":1}`), okc(`{"b":"x"}`), okc(`{}`), okc(`{"a":null,"b":null}`), okc(`{"a":"7","b":"y","extra":1}`),
		badc(`{"a":"x"}`), badc(`{"a":1.5}`), rejectedc(`{"b":1}`),
		kindc(`[1,"x"]`), kindc(`"(1,x)"`), kindc(`1`),
	})
	runColCases(t, "outer_c", true, []colCase{
		okc(`{"i":{"a":1,"b":"x"},"tags":["t1","t2"],"n":1.234,"t":"2026-10-18T14:30:00Z","j":{"k":[1,2]}}`),
		okc(`{"i":{"a":1},"n":9999.99}`), okc(`{"tags":[]}`), okc(`{"tags":null,"j":null}`), okc(`{}`),
		okc(`{"tags":["a","b"],"i":null}`),
		badc(`{"n":10000}`), badc(`{"i":{"a":"x"}}`), badc(`{"t":"not a date"}`), badc(`{"i":[1,2]}`),
	})
	runColCases(t, "inners", true, []colCase{
		okc(`[{"a":1,"b":"x"},{"a":2}]`), okc(`[]`), okc(`[null,{"a":1}]`), okc(`[{}]`),
		badc(`[{"a":"x"}]`), badc(`[1]`),
	})
	runColCases(t, "outers", true, []colCase{
		okc(`[{"i":{"a":1},"tags":["q"],"n":1.5},{"n":2}]`), okc(`[]`), badc(`[{"n":123456}]`),
	})
}

func TestEncode_EnumsAndDomains(t *testing.T) {
	runColCases(t, "mood", true, []colCase{
		okc(`"sad"`), okc(`"happy"`), badc(`"angry"`), badc(`""`), kindc(`1`), kindc(`true`),
	})
	runColCases(t, "moods", true, []colCase{
		okc(`["sad","ok"]`), okc(`[null,"happy"]`), okc(`[]`), badc(`["angry"]`),
	})
	runColCases(t, "pos", true, []colCase{
		okc(`5`), okc(`"7"`), badc(`0`), badc(`-1`), badc(`"x"`), badc(`1.5`),
	})
	runColCases(t, "poss", true, []colCase{
		okc(`[1,2,3]`), okc(`[]`), okc(`[null,2]`), badc(`[0]`), badc(`[1,-1]`),
	})
}

func TestEncode_Modifiers(t *testing.T) {
	// varchar(5) and bpchar(3) are binary types whose modifiers the column's declared type applies
	runColCases(t, "vc", true, []colCase{
		okc(`"abcde"`), okc(`"abc"`), okc(`""`), badc(`"abcdef"`), kindc(`5`),
	})
	runColCases(t, "chr", true, []colCase{
		okc(`"ab"`), okc(`"abc"`), badc(`"abcd"`),
	})
}

func TestEncode_TextMode(t *testing.T) {
	// types without an encoder : the text goes to Postgres' input function, as json_to_record does
	runColCases(t, "iv", false, []colCase{
		okc(`"1 day"`), okc(`"02:00:00"`), okc(`"P1Y2M3DT4H5M6S"`), okc(`"1 year 2 mons"`), okc(`"-1 day"`),
		badc(`"nonsense"`), badc(`"1 foo"`), badc(`""`),
		kindc(`{"a":1}`), // an object's JSON text is not an interval, so both reject : see below for types that accept it
	})
	runColCases(t, "ip", false, []colCase{
		okc(`"10.0.0.1"`), okc(`"10.0.0.1/24"`), okc(`"::1"`), okc(`"2001:db8::/32"`), badc(`"999.1.1.1"`), badc(`"x"`),
	})
	runColCases(t, "rng", false, []colCase{
		okc(`"[1,5)"`), okc(`"(,5]"`), okc(`"empty"`), okc(`"[3,3]"`), badc(`"[5,1)"`), badc(`"x"`), badc(`{"a":1}`),
	})
	runColCases(t, "pt", false, []colCase{
		okc(`"(1,2)"`), okc(`"1,2"`), badc(`"x"`), badc(`[1,2]`), badc(`{"x":1,"y":2}`),
	})
	runColCases(t, "tsv", false, []colCase{
		okc(`"a b c"`), okc(`"'quoted lexeme':1 other:2A"`), okc(`""`),
	})
	runColCases(t, "cash", false, []colCase{
		okc(`"$12.50"`), okc(`"12.5"`), okc(`12.5`), badc(`"abc"`),
	})
	runColCases(t, "bits", false, []colCase{
		okc(`"1010"`), badc(`"101"`), badc(`"10101"`), badc(`"12"`),
	})
	// numbers and booleans go through as their text
	runColCases(t, "iv", false, []colCase{okc(`5`), okc(`1.5`)})
}

func TestEncode_TextModeArraysAndComposites(t *testing.T) {
	runColCases(t, "ivs", false, []colCase{
		okc(`["1 day","02:00:00"]`), okc(`[]`), okc(`[null,"1 day"]`), okc(`["1 day, 2 hours", "3 years"]`),
		okc(`[["1 day","2 days"],["3 days","4 days"]]`), badc(`["nonsense"]`), badc(`[["1 day"],["2 days","3 days"]]`),
	})
	runColCases(t, "ips", false, []colCase{
		okc(`["10.0.0.1","::1"]`), okc(`["10.0.0.0/8", null]`), badc(`["x"]`),
	})
	runColCases(t, "rngs", false, []colCase{
		okc(`["[1,5)","(,3]","empty"]`), okc(`[]`), okc(`[null]`),
	})
	runColCases(t, "ivc", false, []colCase{
		okc(`{"code":"a","span":"1 day"}`), okc(`{"span":"2 hours"}`), okc(`{"code":"with, comma and (parens) and \"quotes\" and \\ slash"}`),
		okc(`{"code":""}`), okc(`{"code":null,"span":null}`), okc(`{}`), okc(`{"code":"x","extra":1}`),
		badc(`{"span":"nonsense"}`), kindc(`["a","1 day"]`),
	})
	runColCases(t, "ivcs", false, []colCase{
		okc(`[{"code":"a","span":"1 day"},{"code":"b, c","span":"2 hours"}]`), okc(`[]`), okc(`[null,{"code":"x"}]`),
		okc(`[{"code":"say \"hi\""}]`), okc(`[{"code":"{braces}"}]`), okc(`[{"code":"NULL"}]`), okc(`[{"code":"back\\slash"}]`),
		badc(`[{"span":"nonsense"}]`),
	})
}

// A column of binary-mode type holds NULL for a missing value : checked by the caller, which never
// hands a null to an encoder. Here, the encoders must not accept one.
func TestEncode_EncodersNeverTakeNull(t *testing.T) {
	cols := []string{"i4s", "inner_c", "mood", "iv", "ivs", "vc"}
	for _, name := range cols {
		col := encT(t).ColumnsMap[name]
		enc, _ := columnEncoder(col.Type)
		if _, err := enc(nil, encNode(t, `null`), &encodeContext{}); err == nil {
			// text-mode scalars write nothing for null ; binary encoders reject it
			if _, binary := columnEncoder(col.Type); binary {
				t.Errorf("%s: a binary encoder accepted null", name)
			}
		}
	}
}

func TestColumnEncoder_ModeSelection(t *testing.T) {
	want := map[string]bool{
		"i4s": true, "txts": true, "nums": true, "uuids": true, "tss": true, "bools": true, "jsonbs": true, "jsons": true,
		"inner_c": true, "outer_c": true, "inners": true, "outers": true, "pos": true, "poss": true, "mood": true, "moods": true,
		"vc": true, "chr": true, "bytes": true, "byteas": true, "id": true,
		"iv": false, "ivs": false, "ivc": false, "ivcs": false, "ip": false, "ips": false, "rng": false, "rngs": false,
		"pt": false, "tsv": false, "cash": false, "bits": false,
	}
	r := encT(t)
	for name, binary := range want {
		col := r.ColumnsMap[name]
		if col == nil {
			t.Errorf("no column %s", name)
			continue
		}
		if _, got := columnEncoder(col.Type); got != binary {
			t.Errorf("%s (%s): binary mode = %v, want %v", name, col.DeclaredType, got, binary)
		}
	}
}

var _ = context.Background
