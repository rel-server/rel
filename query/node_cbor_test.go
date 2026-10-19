package query

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

var cborEncMode = func() cbor.EncMode {
	em, err := cbor.EncOptions{BigIntConvert: cbor.BigIntConvertShortest, Time: cbor.TimeUnix, TimeTag: cbor.EncTagRequired, Sort: cbor.SortCanonical}.EncMode()
	if err != nil {
		panic(err)
	}
	return em
}()

func cborBytes(t testing.TB, v any) []byte {
	t.Helper()
	b, err := cborEncMode.Marshal(v)
	if err != nil {
		t.Fatalf("encoding CBOR: %v", err)
	}
	return b
}

func cborOf(t testing.TB, v any) Node {
	t.Helper()
	n, err := NewCBORNode(cborBytes(t, v))
	if err != nil {
		t.Fatalf("NewCBORNode: %v", err)
	}
	return n
}

var errUnrepresentable = errors.New("not representable in CBOR the way JSON writes it")

var wholeNumber = regexp.MustCompile(`^-?[0-9]+$`)

// jsonToCBORValue turns decoded JSON (numbers kept as text) into the Go value that encodes to the CBOR
// a client would send : whole numbers as integers, others as exact tag 4 decimal fractions.
func jsonToCBORValue(v any) (any, error) {
	switch x := v.(type) {
	case json.Number:
		s := x.String()
		if wholeNumber.MatchString(s) {
			if s == "-0" {
				return nil, errUnrepresentable // an integer has no sign for zero
			}
			b, _ := new(big.Int).SetString(s, 10)
			if b.IsInt64() {
				return b.Int64(), nil
			}
			return b, nil
		}
		mant, exp := s, 0
		if i := strings.IndexAny(s, "eE"); i >= 0 {
			mant = s[:i]
			e, err := parseInt(s[i+1:])
			if err != nil {
				return nil, err
			}
			exp = e
		}
		if dot := strings.IndexByte(mant, '.'); dot >= 0 {
			exp -= len(mant) - dot - 1
			mant = mant[:dot] + mant[dot+1:]
		}
		m, ok := new(big.Int).SetString(mant, 10)
		if !ok {
			return nil, errors.New("bad number " + s)
		}
		if m.Sign() == 0 && strings.HasPrefix(s, "-") {
			return nil, errUnrepresentable
		}
		var mv any = m
		if m.IsInt64() {
			mv = m.Int64()
		}
		return cbor.Tag{Number: 4, Content: []any{int64(exp), mv}}, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			c, err := jsonToCBORValue(e)
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			c, err := jsonToCBORValue(e)
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	}
	return v, nil
}

func parseInt(s string) (int, error) {
	n, err := json.Number(s).Int64()
	return int(n), err
}

// cborFromJSON is the CBOR node a client would send for the JSON text js.
func cborFromJSON(t testing.TB, js string) (Node, error) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(js))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("test fixture is not JSON: %s", js)
	}
	c, err := jsonToCBORValue(v)
	if err != nil {
		return nil, err
	}
	return cborOf(t, c), nil
}

// encSource builds the Node under test from the JSON text of a corpus entry ; ok is false when the
// source cannot say what that text says.
type encSource struct {
	name string
	node func(t *testing.T, js, typeName string) (n Node, ok bool)
}

var encSources = []encSource{
	{"json", func(t *testing.T, js, _ string) (Node, bool) { return encNode(t, js), true }},
	{"cbor", func(t *testing.T, js, typeName string) (Node, bool) {
		if typeName == "json" {
			return nil, false // json keeps the text as written, which a transcoded number does not
		}
		n, err := cborFromJSON(t, js)
		if err != nil {
			if !errors.Is(err, errUnrepresentable) {
				t.Fatalf("transcoding %s: %v", js, err)
			}
			return nil, false
		}
		return n, true
	}},
}

// ---- the adapter -----------------------------------------------------------------------

func TestCBORNode_Kinds(t *testing.T) {
	cases := []struct {
		name string
		v    any
		kind NodeKind
	}{
		{"null", nil, KindNull}, {"true", true, KindBool}, {"int", int64(-5), KindNumber}, {"uint64 max", uint64(math.MaxUint64), KindNumber},
		{"float", 1.5, KindNumber}, {"text", "x", KindString}, {"bytes", []byte{1}, KindBytes},
		{"array", []any{int64(1)}, KindArray}, {"map", map[string]any{"a": int64(1)}, KindObject},
		{"bignum", new(big.Int).Lsh(big.NewInt(1), 100), KindNumber},
		{"negative bignum", new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 100)), KindNumber},
		{"tag 0", cbor.Tag{Number: 0, Content: "2026-10-18T14:30:00Z"}, KindInstant},
		{"tag 1", time.Unix(1700000000, 0), KindInstant},
		{"tag 4", cbor.Tag{Number: 4, Content: []any{int64(-2), int64(150)}}, KindNumber},
		{"tag 4, bignum mantissa", cbor.Tag{Number: 4, Content: []any{int64(3), new(big.Int).Lsh(big.NewInt(1), 90)}}, KindNumber},
		{"tag 4, malformed", cbor.Tag{Number: 4, Content: "x"}, KindInvalid},
		{"tag 4, three items", cbor.Tag{Number: 4, Content: []any{int64(1), int64(2), int64(3)}}, KindInvalid},
		{"tag 37", cbor.Tag{Number: 37, Content: make([]byte, 16)}, KindUUID},
		{"tag 37, wrong length", cbor.Tag{Number: 37, Content: make([]byte, 15)}, KindInvalid},
		{"tag 100", cbor.Tag{Number: 100, Content: int64(20000)}, KindDate},
		{"tag 100, not an integer", cbor.Tag{Number: 100, Content: "x"}, KindInvalid},
		{"unknown tag", cbor.Tag{Number: 999, Content: "x"}, KindInvalid},
	}
	for _, c := range cases {
		n := cborOf(t, c.v)
		if got := n.Kind(); got != c.kind {
			t.Errorf("%s: kind %v, want %v", c.name, got, c.kind)
		}
	}
}

func TestCBORNode_RejectsWhatItMustNot(t *testing.T) {
	cases := map[string][]byte{
		"a map key that is not text": cborBytes(t, map[int]string{1: "a"}),
		"a duplicate key":            {0xa2, 0x61, 'a', 0x01, 0x61, 'a', 0x02},
		"trailing bytes":             {0x01, 0x02},
		"truncated":                  {0x82, 0x01},
		"empty":                      {},
		"nested too deep":            append(repeatByte(0x81, 100), 0x01),
	}
	for name, b := range cases {
		if _, err := NewCBORNode(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// what is allowed
	for name, b := range map[string][]byte{
		"an indefinite-length array": {0x9f, 0x01, 0x02, 0xff},
		"an indefinite-length map":   {0xbf, 0x61, 'a', 0x01, 0xff},
		"a half float":               {0xf9, 0x3c, 0x00},
		"a single float":             {0xfa, 0x3f, 0xc0, 0x00, 0x00},
	} {
		if _, err := NewCBORNode(b); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func repeatByte(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestCBORNode_NumberText(t *testing.T) {
	for _, c := range []struct {
		v          any
		raw, plain string
	}{
		{int64(0), "0", "0"}, {int64(-42), "-42", "-42"}, {uint64(math.MaxUint64), "18446744073709551615", "18446744073709551615"},
		{new(big.Int).Lsh(big.NewInt(1), 100), "1267650600228229401496703205376", "1267650600228229401496703205376"},
		{1.5, "1.5", "1.5"}, {3.0, "3.0", "3"}, {1e21, "1e+21", "1000000000000000000000"}, {0.000001, "1e-06", "0.000001"},
		{math.NaN(), "NaN", "NaN"}, {math.Inf(1), "Infinity", "Infinity"}, {math.Inf(-1), "-Infinity", "-Infinity"},
		{cbor.Tag{Number: 4, Content: []any{int64(-2), int64(150)}}, "150e-2", "1.50"},
		{cbor.Tag{Number: 4, Content: []any{int64(-2), int64(-5)}}, "-5e-2", "-0.05"},
		{cbor.Tag{Number: 4, Content: []any{int64(3), int64(12)}}, "12e3", "12000"},
		{cbor.Tag{Number: 4, Content: []any{int64(0), int64(7)}}, "7e0", "7"},
		{cbor.Tag{Number: 4, Content: []any{int64(-1), int64(0)}}, "0e-1", "0.0"},
		{cbor.Tag{Number: 4, Content: []any{int64(1000), int64(1)}}, "1e1000", "1e1000"},
	} {
		n := cborOf(t, c.v).(*cnode)
		raw, _ := n.Raw()
		plain, _ := n.decimalText()
		if raw != c.raw || plain != c.plain {
			t.Errorf("%v: raw %q plain %q, want %q and %q", c.v, raw, plain, c.raw, c.plain)
		}
	}
	// a float never reads as a whole number
	if _, err := encodeInt(32)(nil, cborOf(t, 3.0), nil); err == nil {
		t.Error("the float 3.0 was accepted for an int4")
	}
}

func TestCBORNode_JSONText(t *testing.T) {
	doc := map[string]any{"b": []any{int64(1), 2.5, nil, true, "x\"y"}, "a": cbor.Tag{Number: 4, Content: []any{int64(-2), int64(150)}}}
	got, err := cborOf(t, doc).(*cnode).jsonText()
	if err != nil || got != `{"a":150e-2,"b":[1,2.5,null,true,"x\"y"]}` {
		t.Errorf("jsonText = %q, %v", got, err)
	}
	for name, v := range map[string]any{
		"a byte string": []byte{1}, "a date and time": time.Unix(1, 0), "an uuid": cbor.Tag{Number: 37, Content: make([]byte, 16)},
		"another tag": cbor.Tag{Number: 999, Content: "x"}, "NaN": math.NaN(), "infinity": math.Inf(1),
		"a byte string inside": map[string]any{"a": []any{[]byte{1}}},
	} {
		if text, err := cborOf(t, v).(*cnode).jsonText(); err == nil {
			t.Errorf("%s: converted to %q", name, text)
		}
	}
}

func TestCBORNode_Accessors(t *testing.T) {
	n := cborOf(t, map[string]any{"a": []any{int64(1), "two"}, "b": nil})
	if v, ok := n.ObjectGet("a"); !ok || v.Kind() != KindArray {
		t.Fatal("ObjectGet a")
	}
	if _, ok := n.ObjectGet("missing"); ok {
		t.Error("ObjectGet found a missing key")
	}
	items, _ := mustGet(t, n, "a").ArrayItems()
	if len(items) != 2 || items[1].Kind() != KindString {
		t.Errorf("items = %v", items)
	}
	if s, err := items[1].StrictString(); err != nil || s != "two" {
		t.Errorf("StrictString = %q, %v", s, err)
	}
	if i, err := items[0].StrictInt64(); err != nil || i != 1 {
		t.Errorf("StrictInt64 = %d, %v", i, err)
	}
	if _, err := items[1].StrictInt64(); err == nil {
		t.Error("StrictInt64 on a string")
	}
	if f, err := cborOf(t, int64(3)).StrictFloat64(); err != nil || f != 3 {
		t.Errorf("StrictFloat64 = %v, %v", f, err)
	}
	seen := 0
	_ = n.ObjectEach(func(string, Node) error { seen++; return nil })
	if seen != 2 {
		t.Errorf("ObjectEach saw %d keys", seen)
	}
}

func mustGet(t *testing.T, n Node, key string) Node {
	t.Helper()
	v, ok := n.ObjectGet(key)
	if !ok {
		t.Fatalf("no key %s", key)
	}
	return v
}

// structureHex builds a document from hex pieces.
func structureHex(t *testing.T, parts ...string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.Join(parts, ""))
	if err != nil {
		t.Fatalf("bad hex: %v", err)
	}
	return b
}

func TestCBORNode_Structures(t *testing.T) {
	const (
		def0 = "d9dfff84" + "19e000" + "82626964646e616d65" // definition of id 0 : ["id", "name"], then 2 values
		ref0 = "d9e00082"                                   // a reference to id 0, then 2 values
	)
	// what cbor-x writes for [{id: 1, name: "aaa"}, {id: 2, name: "bbb"}]
	for name, c := range map[string]struct{ doc, want string }{
		"a definition and a reference": {"82" + def0 + "01" + "63616161" + ref0 + "02" + "63626262",
			`[{"id":1,"name":"aaa"},{"id":2,"name":"bbb"}]`},
		"inside a map": {"a1" + "61" + "78" + def0 + "01" + "f6", `{"x":{"id":1,"name":null}}`},
		"an indefinite array of rows": {"9f" + def0 + "01" + "f6" + ref0 + "02" + "f5" + "ff",
			`[{"id":1,"name":null},{"id":2,"name":true}]`},
		"a nested record": {"82" + "d9dfff83" + "19e000" + "81" + "61" + "61" + "d9dfff83" + "19e001" + "81" + "61" + "62" + "01" + "d9e00081" + "d9e00181" + "02",
			`[{"a":{"b":1}},{"a":{"b":2}}]`},
		// the id is defined again : each row reads with the definition in force when it was written
		"a redefinition": {"82" + def0 + "01" + "f6" + "d9dfff84" + "19e000" + "82616161" + "62" + "f6" + "f5",
			`[{"id":1,"name":null},{"a":null,"b":true}]`},
		"no values": {"d9dfff82" + "19e000" + "80", `{}`},
	} {
		n, err := NewCBORNode(structureHex(t, c.doc))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		got, err := n.Raw()
		if err != nil {
			t.Errorf("%s: Raw: %v", name, err)
			continue
		}
		// the keys of an object are sorted by Raw
		var a, b any
		if json.Unmarshal([]byte(got), &a) != nil || json.Unmarshal([]byte(c.want), &b) != nil || !reflect.DeepEqual(a, b) {
			t.Errorf("%s: got %s, want %s", name, got, c.want)
		}
	}
}

func TestCBORNode_StructureObjectAccess(t *testing.T) {
	n, err := NewCBORNode(structureHex(t, "d9dfff84", "19e000", "82626964646e616d65", "07", "63616161"))
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind() != KindObject {
		t.Fatalf("kind = %v", n.Kind())
	}
	id, ok := n.ObjectGet("id")
	if !ok {
		t.Fatal("no id")
	}
	if v, err := id.StrictInt64(); err != nil || v != 7 {
		t.Errorf("id = %d, %v", v, err)
	}
	var keys []string
	if err := n.ObjectEach(func(k string, _ Node) error { keys = append(keys, k); return nil }); err != nil {
		t.Fatal(err)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"id", "name"}) {
		t.Errorf("keys = %v", keys)
	}
}

func TestCBORNode_RefusesBadStructures(t *testing.T) {
	const defA = "d9dfff83" + "19e000" + "816161" + "01" // id 0 with the one name "a", and its value
	for name, doc := range map[string]string{
		"a reference to an id never defined": "d9e00082" + "01" + "02",
		"a reference with too few values":    "82" + defA + "d9e00080",
		"a reference with too many values":   "82" + defA + "d9e00082" + "01" + "02",
		"a reference that is not an array":   "82" + defA + "d9e000a0",
		"a definition with too few values":   "d9dfff83" + "19e000" + "826161" + "6162" + "01",
		"a definition with too many values":  "d9dfff84" + "19e000" + "80" + "01" + "02",
		"a definition that is not an array":  "d9dfff01",
		"a definition of one item":           "d9dfff81" + "19e000",
		"an id that is not an integer":       "d9dfff83" + "6130" + "816161" + "01",
		"an id below the range":              "d9dfff83" + "19dfff" + "816161" + "01",
		"an id past the range":               "d9dfff83" + "1a00010000" + "816161" + "01",
		"names that are not an array":        "d9dfff83" + "19e000" + "6161" + "01",
		"a name that is not text":            "d9dfff83" + "19e000" + "8101" + "01",
		"names that repeat":                  "d9dfff84" + "19e000" + "826161" + "6161" + "01" + "02",
		"an indefinite-length structure":     "d9dfff9f" + "19e000" + "80" + "ff",
		"a structure nested too deep":        deepStructures(70),
	} {
		_, err := NewCBORNode(structureHex(t, doc))
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// deepStructures is a record whose only value is a record, n levels down.
func deepStructures(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("d9dfff83" + "19e000" + "816161")
	}
	return b.String() + "01"
}

// Tags 0xdffe and 105 are the other record forms cbor-x reads and never writes with the options the TypeScript client uses : they are
// unknown tags, which the parser keeps and a column refuses.
func TestCBORNode_OtherRecordTagsAreUnknown(t *testing.T) {
	for name, doc := range map[string]string{
		"0xdffe": "d9dffe82" + "19e000" + "80",
		"105":    "d8698319e000" + "80" + "80",
	} {
		n, err := NewCBORNode(structureHex(t, doc))
		if err != nil {
			continue // refused at parse time is as good
		}
		if n.Kind() != KindInvalid {
			t.Errorf("tag %s: kind %v", name, n.Kind())
		}
	}
}
