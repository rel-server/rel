package query

import (
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// The production decoder (node_cbor.go) must read every document exactly as the reference does
// (node_cbor_ref_test.go, which rides on a general-purpose CBOR library) : the same tree, or both refusals.

// compareNodes reports the first difference between two trees ("" when they are the same).
func compareNodes(path string, got, want Node) string {
	if got.Kind() != want.Kind() {
		return fmt.Sprintf("%s: kind %d, reference %d", path, got.Kind(), want.Kind())
	}
	switch got.Kind() {
	case KindBool:
		g, _ := got.StrictBool()
		w, _ := want.StrictBool()
		if g != w {
			return fmt.Sprintf("%s: %v, reference %v", path, g, w)
		}
	case KindNumber:
		g, _ := got.Raw()
		w, _ := want.Raw()
		gd, _ := got.(*cnode).decimalText()
		wd, _ := want.(refCBORNode).decimalText()
		if g != w || gd != wd {
			return fmt.Sprintf("%s: number %q/%q, reference %q/%q", path, g, gd, w, wd)
		}
		gi, gerr := got.StrictInt64()
		wi, werr := want.StrictInt64()
		if gi != wi || (gerr == nil) != (werr == nil) {
			return fmt.Sprintf("%s: StrictInt64 %d,%v, reference %d,%v", path, gi, gerr, wi, werr)
		}
	case KindString:
		g, _ := got.StrictString()
		w, _ := want.StrictString()
		if g != w {
			return fmt.Sprintf("%s: %q, reference %q", path, g, w)
		}
	case KindBytes, KindUUID:
		g, _ := got.(*cnode).StrictBytes()
		w, _ := want.(refCBORNode).StrictBytes()
		if string(g) != string(w) {
			return fmt.Sprintf("%s: bytes % x, reference % x", path, g, w)
		}
	case KindInstant:
		g, _ := got.(*cnode).Instant()
		w, _ := want.(refCBORNode).Instant()
		micros := func(t time.Time) int64 { return t.Unix()*1_000_000 + int64((t.Nanosecond()+500)/1000) }
		if micros(g) != micros(w) {
			return fmt.Sprintf("%s: instant %v, reference %v", path, g, w)
		}
	case KindDate:
		g, _ := got.(*cnode).Days()
		w, _ := want.(refCBORNode).Days()
		if g != w {
			return fmt.Sprintf("%s: days %d, reference %d", path, g, w)
		}
	case KindArray:
		g, _ := got.ArrayItems()
		w, _ := want.ArrayItems()
		if len(g) != len(w) {
			return fmt.Sprintf("%s: %d items, reference %d", path, len(g), len(w))
		}
		for i := range g {
			if d := compareNodes(fmt.Sprintf("%s[%d]", path, i), g[i], w[i]); d != "" {
				return d
			}
		}
	case KindObject:
		var gk, wk []string
		_ = got.ObjectEach(func(k string, _ Node) error { gk = append(gk, k); return nil })
		_ = want.ObjectEach(func(k string, _ Node) error { wk = append(wk, k); return nil })
		sort.Strings(gk)
		sort.Strings(wk)
		if fmt.Sprint(gk) != fmt.Sprint(wk) {
			return fmt.Sprintf("%s: keys %v, reference %v", path, gk, wk)
		}
		for _, k := range gk {
			g, _ := got.ObjectGet(k)
			w, _ := want.ObjectGet(k)
			if d := compareNodes(path+"."+k, g, w); d != "" {
				return d
			}
		}
	}
	if gj, ok := got.(*cnode); ok {
		g, gerr := gj.jsonText()
		w, werr := want.(refCBORNode).jsonText()
		if g != w || (gerr == nil) != (werr == nil) {
			return fmt.Sprintf("%s: json %q,%v, reference %q,%v", path, g, gerr, w, werr)
		}
	}
	return ""
}

// containsInvalid reports whether a tree has a node of no kind, which the encoders refuse.
func containsInvalid(n Node) bool {
	switch n.Kind() {
	case KindInvalid:
		return true
	case KindArray:
		items, _ := n.ArrayItems()
		for _, i := range items {
			if containsInvalid(i) {
				return true
			}
		}
	case KindObject:
		bad := false
		_ = n.ObjectEach(func(_ string, v Node) error { bad = bad || containsInvalid(v); return nil })
		return bad
	}
	return false
}

// agree checks one document. A refusal by the reference and a tree with an invalid node in it are the same
// thing for a caller : the value cannot be written.
func agree(t *testing.T, name string, data []byte) (accepted bool) {
	t.Helper()
	got, gotErr := NewCBORNode(data)
	want, wantErr := newRefCBORNode(data)
	switch {
	case gotErr != nil && wantErr != nil:
		return false
	case gotErr != nil:
		if containsInvalid(want) || strings.Contains(gotErr.Error(), "a map key that is not a text string") ||
			strings.Contains(gotErr.Error(), "a tag 0 value that is not an RFC 3339") {
			// the library reads a byte string key as text, and a tag 0 over a string that is no date as that
			// string ; specs/cbor.md refuses the first, and a tag 0 only means a date and time
			return false
		}
		t.Errorf("%s % x: refused (%v), the reference reads it", name, data, gotErr)
		return false
	case wantErr != nil:
		if containsInvalid(got) {
			return true
		}
		t.Errorf("%s % x: read, the reference refuses it (%v)", name, data, wantErr)
		return true
	}
	if usesStructureTags(data) && containsInvalid(want) && !containsInvalid(got) {
		// the reference reads the tags of structures as unknown tags ; TestCBORNode_ReadsWhatCborXWrites is their oracle
		return true
	}
	if d := compareNodes("$", got, want); d != "" {
		t.Errorf("%s % x: %s", name, data, d)
	}
	return true
}

// usesStructureTags reports whether data may hold a structure's tag (specs/cbor.md ## Structures) : a two-byte tag
// of 0xdfff or from 0xe000 up.
func usesStructureTags(data []byte) bool {
	for i := 0; i+2 < len(data); i++ {
		if data[i] == 0xd9 && data[i+1] >= 0xdf {
			return true
		}
	}
	return false
}

func TestCBORDecoder_AllHalfFloats(t *testing.T) {
	for h := 0; h < 1<<16; h++ {
		agree(t, "half", []byte{0xf9, byte(h >> 8), byte(h)})
	}
}

func TestCBORDecoder_Integers(t *testing.T) {
	edges := []uint64{0, 1, 23, 24, 25, 255, 256, 65535, 65536, 1<<32 - 1, 1 << 32, 1<<63 - 1, 1 << 63, 1<<64 - 1}
	for _, major := range []byte{0, 1} {
		for _, v := range edges {
			for _, form := range []int{1, 2, 4, 8} { // every width that holds v, non-shortest included
				if form < 8 && v >= 1<<(8*form) {
					continue
				}
				b := []byte{major<<5 | byte(24+map[int]int{1: 0, 2: 1, 4: 2, 8: 3}[form])}
				for i := form - 1; i >= 0; i-- {
					b = append(b, byte(v>>(8*i)))
				}
				agree(t, "int", b)
			}
		}
	}
}

func TestCBORDecoder_Floats(t *testing.T) {
	for _, f := range []float64{0, math.Copysign(0, -1), 1.5, -1.5, math.MaxFloat64, math.SmallestNonzeroFloat64, math.NaN(), math.Inf(1), math.Inf(-1), 1e21, 0.1, 100} {
		agree(t, "double", append([]byte{0xfb}, cborBytes(t, f)[1:]...))
		agree(t, "single", append([]byte{0xfa}, cborBytes(t, float32(f))[1:]...))
	}
}

func TestCBORDecoder_Strings(t *testing.T) {
	for _, n := range []int{0, 1, 23, 24, 255, 256, 70000} {
		agree(t, "text", cborBytes(t, string(make([]byte, 0))+fmt.Sprintf("%0*d", n, 0)[:n]))
		agree(t, "bytes", cborBytes(t, make([]byte, n)))
	}
	for name, b := range map[string][]byte{
		"multibyte":                       cborBytes(t, "héllo ✓ 日本語 😀"),
		"invalid UTF-8":                   {0x62, 0xc3, 0x28},
		"overlong UTF-8":                  {0x62, 0xc0, 0x80},
		"a surrogate":                     {0x63, 0xed, 0xa0, 0x80},
		"indefinite text":                 {0x7f, 0x61, 'a', 0x62, 'b', 'c', 0xff},
		"indefinite bytes":                {0x5f, 0x41, 1, 0x42, 2, 3, 0xff},
		"empty indefinite text":           {0x7f, 0xff},
		"a chunk of the wrong major type": {0x7f, 0x41, 'a', 0xff},
		"a nested indefinite chunk":       {0x7f, 0x7f, 0xff, 0xff},
		"a split multibyte character":     {0x7f, 0x61, 0xc3, 0x61, 0xa9, 0xff},
		"a string that runs past the end": {0x65, 'a', 'b'},
		"an unterminated indefinite text": {0x7f, 0x61, 'a'},
		"a huge length":                   {0x7b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 'a'},
	} {
		agree(t, name, b)
	}
}

func TestCBORDecoder_Containers(t *testing.T) {
	nest := func(depth int, leaf byte) []byte { return append(repeatByte(0x81, depth), leaf) }
	for _, depth := range []int{1, 62, 63, 64, 65, 66, 100} {
		agree(t, fmt.Sprintf("array nested %d", depth), nest(depth, 0x01))
	}
	mapNest := func(depth int) []byte {
		var b []byte
		for i := 0; i < depth; i++ {
			b = append(b, 0xa1, 0x61, 'k')
		}
		return append(b, 0x01)
	}
	for _, depth := range []int{1, 63, 64, 65} {
		agree(t, fmt.Sprintf("map nested %d", depth), mapNest(depth))
	}
	for name, b := range map[string][]byte{
		"empty array":                     {0x80},
		"empty map":                       {0xa0},
		"indefinite array":                {0x9f, 0x01, 0x02, 0xff},
		"indefinite map":                  {0xbf, 0x61, 'a', 0x01, 0xff},
		"empty indefinite array":          {0x9f, 0xff},
		"indefinite inside definite":      {0x82, 0x9f, 0x01, 0xff, 0x02},
		"duplicate keys":                  {0xa2, 0x61, 'a', 0x01, 0x61, 'a', 0x02},
		"duplicate keys, indefinite":      {0xbf, 0x61, 'a', 0x01, 0x61, 'a', 0x02, 0xff},
		"an integer key":                  {0xa1, 0x01, 0x02},
		"a bytes key":                     {0xa1, 0x41, 'a', 0x02},
		"a chunked text key":              {0xa1, 0x7f, 0x61, 'a', 0x61, 'b', 0xff, 0x01},
		"a map without its last value":    {0xa1, 0x61, 'a'},
		"a break with nothing to break":   {0xff},
		"a count past the data":           {0x9b, 0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01},
		"a map count past the data":       {0xb9, 0xff, 0xff, 0x01},
		"an unterminated array":           {0x9f, 0x01},
		"an unterminated map":             {0xbf, 0x61, 'a', 0x01},
		"trailing bytes":                  {0x01, 0x02},
		"nothing":                         {},
		"a break inside a definite array": {0x82, 0x01, 0xff},
	} {
		agree(t, name, b)
	}
	// more than eight keys takes the other duplicate check
	many := map[string]any{}
	for i := 0; i < 40; i++ {
		many[fmt.Sprintf("k%d", i)] = int64(i)
	}
	agree(t, "forty keys", cborBytes(t, many))
	dup := []byte{0xb8, 0x14}
	for i := 0; i < 20; i++ {
		dup = append(dup, 0x62, 'k', byte('a'+i%19), 0x01) // 19 distinct names for 20 keys : one repeats
	}
	agree(t, "twenty keys with a repeat", dup)
}

func TestCBORDecoder_Tags(t *testing.T) {
	huge := new(big.Int).Lsh(big.NewInt(1), 200)
	tagged := func(n uint64, content any) []byte { return cborBytes(t, cbor.Tag{Number: n, Content: content}) }
	for name, b := range map[string][]byte{
		"tag 0":                 tagged(0, "2026-10-18T14:30:00Z"),
		"tag 0 with an offset":  tagged(0, "2026-10-18T14:30:00.123456+02:00"),
		"tag 0 not a date":      tagged(0, "not a date"),
		"tag 0 over a number":   tagged(0, int64(1)),
		"tag 1 integer":         tagged(1, int64(1700000000)),
		"tag 1 negative":        tagged(1, int64(-1)),
		"tag 1 float":           tagged(1, 1700000000.5),
		"tag 1 float negative":  tagged(1, -1.5),
		"tag 1 huge":            tagged(1, uint64(1)<<63),
		"tag 1 over text":       tagged(1, "x"),
		"tag 2":                 {0xc2, 0x49, 1, 0, 0, 0, 0, 0, 0, 0, 0},
		"tag 2 empty":           {0xc2, 0x40},
		"tag 2 leading zeros":   {0xc2, 0x43, 0, 0, 5},
		"tag 3":                 {0xc3, 0x49, 1, 0, 0, 0, 0, 0, 0, 0, 0},
		"tag 3 empty":           {0xc3, 0x40},
		"tag 2 over text":       tagged(2, "x"),
		"tag 2 chunked":         {0xc2, 0x5f, 0x41, 1, 0x41, 2, 0xff},
		"tag 4":                 tagged(4, []any{int64(-2), int64(150)}),
		"tag 4 negative":        tagged(4, []any{int64(-2), int64(-150)}),
		"tag 4 positive exp":    tagged(4, []any{int64(5), int64(7)}),
		"tag 4 bignum mantissa": tagged(4, []any{int64(-3), huge}),
		"tag 4 negative bignum": tagged(4, []any{int64(-3), new(big.Int).Neg(huge)}),
		"tag 4 uint64 mantissa": tagged(4, []any{int64(0), uint64(math.MaxUint64)}),
		"tag 4 float mantissa":  tagged(4, []any{int64(0), 1.5}),
		"tag 4 float exponent":  tagged(4, []any{1.5, int64(1)}),
		"tag 4 one item":        tagged(4, []any{int64(1)}),
		"tag 4 text":            tagged(4, "x"),
		"tag 4 nested":          tagged(4, []any{int64(0), []any{int64(1)}}),
		"tag 5":                 tagged(5, []any{int64(-1), int64(3)}),
		"tag 37":                tagged(37, make([]byte, 16)),
		"tag 37 short":          tagged(37, make([]byte, 15)),
		"tag 37 text":           tagged(37, "0123456789abcdef"),
		"tag 100":               tagged(100, int64(20000)),
		"tag 100 negative":      tagged(100, int64(-719162)),
		"tag 100 text":          tagged(100, "x"),
		"tag 100 huge":          tagged(100, uint64(1)<<63),
		"an unknown tag":        tagged(999, "x"),
		"a tag over a tag":      tagged(999, cbor.Tag{Number: 1000, Content: int64(1)}),
		"tag 4 over tag 4":      tagged(4, []any{int64(0), cbor.Tag{Number: 4, Content: []any{int64(0), int64(1)}}}),
		"tag 1 inside an array": cborBytes(t, []any{cbor.Tag{Number: 1, Content: int64(5)}, cbor.Tag{Number: 999, Content: int64(5)}}),
		"tag 1 inside a map":    cborBytes(t, map[string]any{"a": cbor.Tag{Number: 100, Content: int64(5)}}),
		"a huge tag number":     {0xdb, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01},
		"a tag with nothing":    {0xc2},
	} {
		agree(t, name, b)
	}
}

// a tag 1 that is not a finite instant is refused outright (the reference reads it as some date)
func TestCBORDecoder_RefusesNonFiniteInstants(t *testing.T) {
	for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 1e300, 1e19} {
		if _, err := NewCBORNode(cborBytes(t, cbor.Tag{Number: 1, Content: f})); err == nil {
			t.Errorf("tag 1 %v was accepted", f)
		}
	}
}

func TestCBORDecoder_SimpleValues(t *testing.T) {
	for _, b := range [][]byte{{0xf4}, {0xf5}, {0xf6}, {0xf7}, {0xf0}, {0xf3}, {0xf8, 0x20}, {0xf8, 0xff}, {0xf8, 0x10}, {0xfc}, {0xfd}, {0xfe}} {
		agree(t, "simple", b)
	}
}

func TestCBORDecoder_EveryTruncation(t *testing.T) {
	doc := map[string]any{
		"a": []any{int64(1), "text", []byte{1, 2, 3}, 1.5, nil, true, cbor.Tag{Number: 4, Content: []any{int64(-2), int64(150)}}},
		"b": map[string]any{"c": cbor.Tag{Number: 37, Content: make([]byte, 16)}, "d": new(big.Int).Lsh(big.NewInt(1), 100)},
	}
	full := cborBytes(t, doc)
	for n := 0; n <= len(full); n++ {
		agree(t, fmt.Sprintf("prefix %d of %d", n, len(full)), full[:n])
	}
	agree(t, "trailing", append(append([]byte{}, full...), 0x00))
}

// randomValue builds a nested value out of everything the Go side can encode.
func randomValue(r *rand.Rand, depth int) any {
	kinds := 14
	if depth > 3 {
		kinds = 10
	}
	switch r.Intn(kinds) {
	case 0:
		return nil
	case 1:
		return r.Intn(2) == 0
	case 2:
		return r.Int63() - r.Int63()
	case 3:
		return r.Uint64()
	case 4:
		return r.NormFloat64() * math.Pow(10, float64(r.Intn(40)-20))
	case 5:
		return string(make([]byte, 0)) + fmt.Sprintf("s%d héllo", r.Intn(1000))
	case 6:
		b := make([]byte, r.Intn(40))
		r.Read(b)
		return b
	case 7:
		return new(big.Int).Lsh(big.NewInt(r.Int63()), uint(r.Intn(200)))
	case 8:
		return cbor.Tag{Number: 4, Content: []any{int64(r.Intn(40) - 20), r.Int63() - r.Int63()}}
	case 9:
		return []cbor.Tag{{Number: 1, Content: r.Int63n(1 << 40)}, {Number: 100, Content: int64(r.Intn(100000) - 50000)}, {Number: 37, Content: make([]byte, 16)}, {Number: 0, Content: "2026-10-18T14:30:00Z"}}[r.Intn(4)]
	case 10, 11:
		a := make([]any, r.Intn(6))
		for i := range a {
			a[i] = randomValue(r, depth+1)
		}
		return a
	}
	m := map[string]any{}
	for i := r.Intn(12); i > 0; i-- {
		m[fmt.Sprintf("k%d", r.Intn(20))] = randomValue(r, depth+1)
	}
	return m
}

// TestCBORDecoder_RandomDocuments reads random documents, and the same documents with bytes changed.
func TestCBORDecoder_RandomDocuments(t *testing.T) {
	accepted, refused := 0, 0
	for seed := int64(1); seed <= 12; seed++ {
		r := rand.New(rand.NewSource(seed))
		a, f := randomRound(t, r, 1500)
		accepted, refused = accepted+a, refused+f
		if t.Failed() {
			return
		}
	}
	t.Logf("mutated documents : %d read, %d refused", accepted, refused)
	if accepted < 1000 || refused < 1000 {
		t.Errorf("the mutations should produce both kinds of documents")
	}
}

func randomRound(t *testing.T, r *rand.Rand, documents int) (accepted, refused int) {
	t.Helper()
	for i := 0; i < documents; i++ {
		doc := cborBytes(t, randomValue(r, 0))
		if !agree(t, "random", doc) {
			t.Fatalf("a document the encoder wrote was refused : % x", doc)
		}
		for m := 0; m < 6; m++ {
			mut := append([]byte{}, doc...)
			for k := r.Intn(3) + 1; k > 0 && len(mut) > 0; k-- {
				switch r.Intn(3) {
				case 0:
					mut[r.Intn(len(mut))] = byte(r.Intn(256))
				case 1:
					mut = append(mut[:r.Intn(len(mut))], mut[min(len(mut), r.Intn(len(mut))+1):]...)
				case 2:
					at := r.Intn(len(mut) + 1)
					mut = append(mut[:at], append([]byte{byte(r.Intn(256))}, mut[at:]...)...)
				}
			}
			if agree(t, "mutated", mut) {
				accepted++
			} else {
				refused++
			}
		}
		if t.Failed() {
			return
		}
	}
	return
}
