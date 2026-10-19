package cborenc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

// The library is only the oracle here : what it decodes must be what was meant.

func decode(t *testing.T, b []byte) any {
	t.Helper()
	dm, err := cbor.DecOptions{TagsMd: cbor.TagsAllowed, TimeTag: cbor.DecTagIgnored, IntDec: cbor.IntDecConvertSignedOrBigInt, MaxNestedLevels: 1100}.DecMode()
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := dm.Unmarshal(b, &v); err != nil {
		t.Fatalf("not valid CBOR: % x : %v", b[:min(len(b), 40)], err)
	}
	return v
}

func TestHeadsAreTheShortestForm(t *testing.T) {
	edges := []uint64{0, 1, 23, 24, 25, 255, 256, 65535, 65536, 1<<32 - 1, 1 << 32, 1<<63 - 1, 1 << 63, math.MaxUint64}
	for _, v := range edges {
		want, _ := cbor.Marshal(v)
		if got := AppendUint(nil, v); !bytes.Equal(got, want) {
			t.Errorf("uint %d: % x, want % x", v, got, want)
		}
	}
	for _, v := range []int64{0, -1, -24, -25, -256, -257, -65536, -65537, math.MinInt32, math.MinInt32 - 1, math.MinInt64, math.MaxInt64, 5} {
		want, _ := cbor.Marshal(v)
		if got := AppendInt(nil, v); !bytes.Equal(got, want) {
			t.Errorf("int %d: % x, want % x", v, got, want)
		}
	}
	for _, f := range []float64{0, math.Copysign(0, -1), 1.5, -1.5, math.MaxFloat64, math.SmallestNonzeroFloat64, math.NaN(), math.Inf(1), math.Inf(-1)} {
		want, _ := cbor.EncOptions{ShortestFloat: cbor.ShortestFloatNone, NaNConvert: cbor.NaNConvertNone, InfConvert: cbor.InfConvertNone}.EncMode()
		w, _ := want.Marshal(f)
		if got := AppendFloat64(nil, f); !bytes.Equal(got, w) {
			t.Errorf("float %v: % x, want % x", f, got, w)
		}
	}
	for _, s := range []string{"", "a", strings.Repeat("é", 200), "日本語 😀"} {
		wantT, _ := cbor.Marshal(s)
		if got := AppendText(nil, s); !bytes.Equal(got, wantT) {
			t.Errorf("text %q", s)
		}
		wantB, _ := cbor.Marshal([]byte(s))
		if got := AppendBytes(nil, []byte(s)); !bytes.Equal(got, wantB) {
			t.Errorf("bytes %q", s)
		}
	}
	if !bytes.Equal(AppendBool(AppendBool(AppendNull(nil), true), false), []byte{0xf6, 0xf5, 0xf4}) {
		t.Error("null, true, false")
	}
}

func bigOf(t *testing.T, v any) *big.Int {
	t.Helper()
	switch x := v.(type) {
	case int64:
		return big.NewInt(x)
	case uint64:
		return new(big.Int).SetUint64(x)
	case big.Int:
		return &x
	}
	t.Fatalf("not an integer : %T %v", v, v)
	return nil
}

func TestAppendInteger(t *testing.T) {
	for _, s := range []string{
		"0", "-0", "00", "7", "-7", "18446744073709551615", "18446744073709551616", "-18446744073709551616", "-18446744073709551617",
		"9223372036854775807", "-9223372036854775808", "9223372036854775808", "123456789012345678901234567890", "-123456789012345678901234567890",
		"1" + strings.Repeat("0", 100), "-" + "1" + strings.Repeat("0", 100), "0012", "9999999999999999999", "10000000000000000000",
	} {
		got := bigOf(t, decode(t, AppendInteger(nil, s)))
		want, _ := new(big.Int).SetString(s, 10)
		if got.Cmp(want) != 0 {
			t.Errorf("%s decodes to %s", s, got)
		}
	}
	// the integers of 64 bits stay integers, only the rest take a tag
	if b := AppendInteger(nil, "18446744073709551615"); b[0] != 0x1b {
		t.Errorf("2^64-1 : % x", b)
	}
	if b := AppendInteger(nil, "-18446744073709551616"); b[0] != 0x3b {
		t.Errorf("-2^64 : % x", b)
	}
	if b := AppendInteger(nil, "18446744073709551616"); b[0] != 0xc2 {
		t.Errorf("2^64 : % x", b)
	}
	if b := AppendInteger(nil, "-18446744073709551617"); b[0] != 0xc3 {
		t.Errorf("-2^64-1 : % x", b)
	}
}

func TestAppendDecimalAndTags(t *testing.T) {
	tag := decode(t, AppendDecimal(nil, "-150", -2)).(cbor.Tag)
	pair := tag.Content.([]any)
	if tag.Number != 4 || bigOf(t, pair[0]).Int64() != -2 || bigOf(t, pair[1]).Int64() != -150 {
		t.Errorf("decimal = %v", tag)
	}
	big := AppendDecimal(nil, "1"+strings.Repeat("0", 40), 3)
	if tag := decode(t, big).(cbor.Tag); bigOf(t, tag.Content.([]any)[1]).String() != "1"+strings.Repeat("0", 40) {
		t.Errorf("bignum mantissa = %v", tag)
	}
	// tag 1 is checked as bytes : a library would turn it into a time
	if b := AppendEpoch(nil, 1700000000, 0); b[0] != 0xc1 || bigOf(t, decode(t, b[1:])).Int64() != 1700000000 {
		t.Errorf("epoch = % x", b)
	}
	if b := AppendEpoch(nil, 1700000000, 500000); b[0] != 0xc1 || decode(t, b[1:]).(float64) != 1700000000.5 {
		t.Errorf("epoch with a fraction = % x", b)
	}
	if tag := decode(t, AppendUUID(nil, [16]byte{1, 2, 3})).(cbor.Tag); tag.Number != 37 || len(tag.Content.([]byte)) != 16 {
		t.Errorf("uuid = %v", tag)
	}
	if tag := decode(t, AppendEpochDate(nil, -719162)).(cbor.Tag); tag.Number != 100 || bigOf(t, tag.Content).Int64() != -719162 {
		t.Errorf("date = %v", tag)
	}
	arr := AppendInt(AppendInt(AppendArrayHead(nil, 2), 1), 2)
	if got, ok := decode(t, arr).([]any); !ok || len(got) != 2 {
		t.Errorf("array head : % x", arr)
	}
	m := AppendInt(AppendText(AppendMapHead(nil, 1), "k"), 7)
	if got, ok := decode(t, m).(map[any]any); !ok || got["k"] != int64(7) {
		t.Errorf("map head : % x", m)
	}
}

// same checks whether a decoded CBOR value is what the JSON text said
func sameValue(t *testing.T, path string, cb any, js any) {
	t.Helper()
	switch j := js.(type) {
	case nil:
		if cb != nil {
			t.Errorf("%s: %v, want null", path, cb)
		}
	case bool:
		if cb != j {
			t.Errorf("%s: %v, want %v", path, cb, j)
		}
	case string:
		if cb != j {
			t.Errorf("%s: %q, want %q", path, cb, j)
		}
	case json.Number:
		want, ok := new(big.Rat).SetString(j.String())
		if !ok {
			t.Fatalf("test number %s", j)
		}
		var got *big.Rat
		if tag, isTag := cb.(cbor.Tag); isTag {
			if tag.Number != 4 {
				t.Errorf("%s: tag %d", path, tag.Number)
				return
			}
			pair := tag.Content.([]any)
			exp, mant := bigOf(t, pair[0]).Int64(), bigOf(t, pair[1])
			got = new(big.Rat).SetInt(mant)
			scale := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(abs64(exp)), nil))
			if exp >= 0 {
				got.Mul(got, scale)
			} else {
				got.Quo(got, scale)
			}
			// a number with a '.' or an exponent must come as a decimal, never as an integer
		} else {
			got = new(big.Rat).SetInt(bigOf(t, cb))
			if strings.ContainsAny(j.String(), ".eE") {
				t.Errorf("%s: %s came as an integer", path, j)
			}
		}
		if got.Cmp(want) != 0 {
			t.Errorf("%s: %v, want %s", path, got, j)
		}
	case []any:
		c, ok := cb.([]any)
		if !ok || len(c) != len(j) {
			t.Errorf("%s: %v, want array of %d", path, cb, len(j))
			return
		}
		for i := range j {
			sameValue(t, fmt.Sprintf("%s[%d]", path, i), c[i], j[i])
		}
	case map[string]any:
		c, ok := cb.(map[any]any)
		if !ok {
			cm, ok2 := cb.(map[string]any)
			if !ok2 {
				t.Errorf("%s: %T, want map", path, cb)
				return
			}
			c = map[any]any{}
			for k, v := range cm {
				c[k] = v
			}
		}
		if len(c) != len(j) {
			t.Errorf("%s: %d keys, want %d", path, len(c), len(j))
		}
		for k, v := range j {
			sameValue(t, path+"."+k, c[k], v)
		}
	default:
		t.Fatalf("test value %T", js)
	}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func checkJSON(t *testing.T, src string) {
	t.Helper()
	out, err := AppendJSON(nil, []byte(src))
	if err != nil {
		t.Errorf("%s: %v", trunc(src), err)
		return
	}
	dec := json.NewDecoder(strings.NewReader(src))
	dec.UseNumber()
	var want any
	if err := dec.Decode(&want); err != nil {
		t.Fatalf("test fixture is not JSON: %s", trunc(src))
	}
	sameValue(t, "$", decode(t, out), want)
}

func trunc(s string) string {
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

func TestAppendJSON_Documents(t *testing.T) {
	for _, src := range []string{
		`null`, `true`, `false`, `0`, `-0`, `1`, `-1`, `1.5`, `-1.50`, `0.0`, `1e3`, `1E3`, `1e+3`, `1e-3`, `1.25e2`, `-1.25E-2`, `0.1e1`,
		`18446744073709551615`, `18446744073709551616`, `-9223372036854775808`, `-18446744073709551616`, `-18446744073709551617`,
		`123456789012345678901234567890.123456789`, `1e400`, `1e-400`, `0.000000000000000000000000000001`,
		`""`, `"a"`, `"é ✓ 日本語 😀"`, `"\"\\\/\b\f\n\r\t"`, `"Aé€"`, `"😀"`, `"a\u0000b"`,
		`[]`, `{}`, `[[]]`, `[{}]`, `{"a":[]}`, `[1,2,3]`, ` [ 1 , 2 ] `, `{"a":1,"b":[true,false,null],"c":{"d":"e"}}`,
		`{"":1}`, `{"a":{"b":{"c":{"d":[1,[2,[3,[4]]]]}}}}`, "\t\n\r [\n1\n]\n",
	} {
		checkJSON(t, src)
	}
	// 1000 levels pass, 1001 do not
	checkJSON(t, strings.Repeat("[", 1000)+strings.Repeat("]", 1000))
	if _, err := AppendJSON(nil, []byte(strings.Repeat("[", 1001)+strings.Repeat("]", 1001))); err == nil {
		t.Error("1001 levels were accepted")
	}
}

func TestAppendJSON_Strings(t *testing.T) {
	cases := map[string]string{
		`"\ud83d"`:        "�", // a lone high surrogate
		`"\ude00"`:        "�", // a lone low surrogate
		`"\ud83dA"`:       "�A",
		`"\ud83d\ud83d"`:  "��",
		"\"\xff\xfe\"":    "��", // bytes that are no UTF-8
		"\"a\xc3\"":       "a�",
		`"é"`:             "é",
		"\"caf\xc3\xa9\"": "café",
		`"😀x"`:            "😀x",
	}
	for src, want := range cases {
		out, err := AppendJSON(nil, []byte(src))
		if err != nil {
			t.Errorf("%q: %v", src, err)
			continue
		}
		if got := decode(t, out).(string); got != want {
			t.Errorf("%q: %q, want %q", src, got, want)
		}
	}
}

func TestAppendJSON_Refusals(t *testing.T) {
	for _, src := range []string{
		``, ` `, `nul`, `nulll`, `tru`, `True`, `[1,]`, `[,1]`, `[1 2]`, `{"a"}`, `{"a":}`, `{"a":1,}`, `{a:1}`, `{"a" 1}`, `{1:2}`, `[`, `]`, `{`, `}`,
		`[1}`, `{"a":1]`, `"abc`, `"a\`, `"a\x"`, `"\u12"`, `"\uzzzz"`, "\"a\nb\"", "\"a\tb\"", `01`, `-`, `1.`, `.5`, `1e`, `1e+`, `+1`, `--1`, `1.5.5`,
		`0x10`, `NaN`, `Infinity`, `-Infinity`, `[1] [2]`, `1 2`, `"a" "b"`, `{"a":1} x`, `1e99999999999999999999`,
	} {
		before := []byte("keep")
		out, err := AppendJSON(before, []byte(src))
		if err == nil {
			t.Errorf("%q was accepted", src)
		}
		if string(out) != "keep" {
			t.Errorf("%q: dst changed to %q", src, out)
		}
	}
}

func randomJSON(r *rand.Rand, depth int) string {
	kinds := 8
	if depth > 3 {
		kinds = 5
	}
	switch r.Intn(kinds) {
	case 0:
		return "null"
	case 1:
		return []string{"true", "false"}[r.Intn(2)]
	case 2:
		return fmt.Sprintf("%d", r.Int63()-r.Int63())
	case 3:
		digits := func(n int) string {
			b := make([]byte, n)
			for i := range b {
				b[i] = byte('0' + r.Intn(10))
			}
			return string(b)
		}
		s := []string{"", "-"}[r.Intn(2)] + []string{"0", strings.TrimLeft(digits(1+r.Intn(25)), "0") + "1"}[r.Intn(2)]
		if r.Intn(2) == 0 {
			s += "." + digits(1+r.Intn(20))
		}
		if r.Intn(3) == 0 {
			s += fmt.Sprintf("e%d", r.Intn(60)-30)
		}
		return s
	case 4:
		b, _ := json.Marshal(fmt.Sprintf("s%d \"q\" \\ é\n\t%c", r.Intn(100), rune(0x20+r.Intn(0x2000))))
		return string(b)
	case 5, 6:
		parts := make([]string, r.Intn(5))
		for i := range parts {
			parts[i] = randomJSON(r, depth+1)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	parts := make([]string, r.Intn(5))
	for i := range parts {
		k, _ := json.Marshal(fmt.Sprintf("k%d", i))
		parts[i] = string(k) + ":" + randomJSON(r, depth+1)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func TestAppendJSON_RandomDocuments(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	for i := 0; i < 3000; i++ {
		checkJSON(t, randomJSON(r, 0))
		if t.Failed() {
			return
		}
	}
}

// AppendJSON takes exactly the texts encoding/json calls valid.
func TestAppendJSON_AcceptsWhatJSONAccepts(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	accepted, refused := 0, 0
	for i := 0; i < 4000; i++ {
		doc := []byte(randomJSON(r, 0))
		for m := 0; m < 12; m++ {
			mut := append([]byte{}, doc...)
			for k := r.Intn(3) + 1; k > 0 && len(mut) > 0; k-- {
				switch r.Intn(3) {
				case 0:
					alphabet := " \t\n[]{},:\"\\-+.eE0123456789tfnul"
					mut[r.Intn(len(mut))] = alphabet[r.Intn(len(alphabet))]
				case 1:
					at := r.Intn(len(mut))
					mut = append(mut[:at], mut[at+1:]...)
				case 2:
					at := r.Intn(len(mut) + 1)
					mut = append(mut[:at], append([]byte{"[]{},:\"01-.e"[r.Intn(11)]}, mut[at:]...)...)
				}
			}
			_, err := AppendJSON(nil, mut)
			if err != nil && strings.Contains(err.Error(), "an exponent out of range") {
				continue // valid JSON, but an exponent a tag 4 cannot hold
			}
			if valid := json.Valid(mut); valid != (err == nil) {
				t.Fatalf("%q : json.Valid %v, AppendJSON error %v", mut, valid, err)
			}
			if err == nil {
				accepted++
			} else {
				refused++
			}
		}
	}
	t.Logf("%d accepted, %d refused", accepted, refused)
	if accepted < 500 || refused < 500 {
		t.Error("the mutations should give both kinds of text")
	}
}

func TestAccepts(t *testing.T) {
	for accept, want := range map[string]bool{
		"":                                   false,
		"application/json":                   false,
		"application/cbor":                   true,
		"*/*":                                false,
		"application/*":                      false,
		"text/html":                          false,
		"application/cbor, application/json": false, // a tie answers JSON
		"application/json, application/cbor": false,
		"application/cbor, application/json;q=0.9":                     true,
		"application/json, application/cbor;q=0.9":                     false,
		"application/cbor;q=0":                                         false,
		"application/cbor;q=0.5, */*;q=0.1":                            true,
		"application/json;q=0, application/cbor":                       true,
		"application/cbor, */*":                                        true,
		"*/*;q=0.8, application/cbor":                                  true,
		"Application/CBOR":                                             true,
		"application/cbor;q=0.2, application/json;q=0.2":               false,
		"application/cbor;foo=bar":                                     true,
		"garbage,,;;":                                                  false,
		"application/json;q=0.1, text/*;q=0.9, application/cbor;q=0.1": false,
	} {
		if got := Accepts(accept); got != want {
			t.Errorf("Accepts(%q) = %v, want %v", accept, got, want)
		}
	}
}

func TestNegotiate(t *testing.T) {
	for accept, want := range map[string]Format{
		"":                                   JSON,
		"application/cbor":                   CBOR,
		"application/cbor; structures=1":     CBORStructures,
		"application/cbor;structures=true":   CBORStructures,
		"application/cbor; structures=0":     CBOR,
		"application/cbor; structures=maybe": CBOR,
		"application/cbor, application/cbor; structures=1":                         CBORStructures, // a tie goes to the parameter
		"application/cbor;q=0.9, application/cbor; structures=1;q=0.5":             CBOR,
		"application/cbor; structures=1;q=0.5, application/cbor;q=0.9":             CBOR,
		"application/cbor; structures=1;q=0.9, application/cbor;q=0.5":             CBORStructures,
		"application/cbor; structures=1, application/json":                         JSON, // a tie with JSON
		"application/cbor; structures=1, application/json;q=0.9":                   CBORStructures,
		"application/cbor; structures=1;q=0":                                       JSON,
		"application/cbor; structures=1;q=0, application/cbor;q=0.5":               CBOR,
		"application/cbor; structures=1, */*":                                      CBORStructures,
		"*/*":                                                                      JSON,
		"application/json;q=0, */*":                                                CBOR, // a wildcard never asks for the parameter
		"application/cbor; structures=1;q=0.5, application/json;q=0.9":             JSON,
		"application/cbor; structures=1;q=0.1, text/*;q=0.9, application/json;q=0": CBORStructures,
	} {
		if got := Negotiate(accept); got != want {
			t.Errorf("Negotiate(%q) = %v, want %v", accept, got, want)
		}
	}
}

func TestFormatMediaType(t *testing.T) {
	for f, want := range map[Format]string{JSON: "application/json", CBOR: "application/cbor", CBORStructures: "application/cbor; structures=1"} {
		if got := f.MediaType(); got != want {
			t.Errorf("Format(%d).MediaType() = %q, want %q", f, got, want)
		}
	}
}

func TestAppendStructure(t *testing.T) {
	// the bytes cbor-x writes for [{id: 1, name: "aaa"}, {id: 2, name: "bbb"}]
	want := "82" + "d9dfff84" + "19e000" + "82626964646e616d65" + "01" + "63616161" + "d9e00082" + "02" + "63626262"
	out := []byte{0x82}
	out = AppendStructureDef(out, 0, []string{"id", "name"})
	out = AppendUint(out, 1)
	out = AppendText(out, "aaa")
	out = AppendStructureRef(out, 0, 2)
	out = AppendUint(out, 2)
	out = AppendText(out, "bbb")
	if got := hex.EncodeToString(out); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
