package cborenc

import (
	"bytes"
	"encoding/hex"
	"math/rand"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

func TestWellFormed_Cases(t *testing.T) {
	deep := bytes.Repeat([]byte{0x81}, 1000) // a thousand nested arrays, deeper than the stack's backing
	for _, c := range []struct {
		hex  string
		want bool
	}{
		{"", false},
		{"00", true},
		{"0000", false},                  // two items
		{"18", false},                    // a head cut short
		{"1c", false},                    // reserved additional information
		{"1f", false},                    // an indefinite integer
		{"f818", false},                  // a two-byte simple value below 32
		{"f820", true},                   // an unassigned simple value
		{"ff", false},                    // a break outside any container
		{"42aa", false},                  // a byte string cut short
		{"5f4101420203ff", true},         // an indefinite byte string of two chunks
		{"5f6161ff", false},              // a text chunk in a byte string
		{"5f5f4101ffff", false},          // an indefinite chunk
		{"9f0102ff", true},               // an indefinite array
		{"bf6161ff", false},              // a key with no value before the break
		{"bf616101ff", true},             // an indefinite map
		{"a20102", false},                // a map short of a pair
		{"9fc1ff", false},                // a break where a tag's content should be
		{"c1", false},                    // a tag and nothing
		{"d9dfff83190e0081616101", true}, // a cbor-x structure definition is plain CBOR
		{"8100ff", false},                // a break after a definite array
		{"9b00000000ffffffff", false},    // an array longer than the bytes left
		{hex.EncodeToString(deep) + "00", true},
		{hex.EncodeToString(deep), false},
	} {
		b, err := hex.DecodeString(c.hex)
		if err != nil {
			t.Fatal(err)
		}
		if got := WellFormed(b); got != c.want {
			t.Errorf("%s: WellFormed is %v", shortHex(c.hex), got)
		}
	}
}

// Against fxamacker/cbor, whose only differences are its limits and UTF-8 check, lifted here.
func TestWellFormed_AgreesWithFxamacker(t *testing.T) {
	dm, err := cbor.DecOptions{
		MaxNestedLevels: 65535, MaxArrayElements: 1 << 30, MaxMapPairs: 1 << 30,
		UTF8: cbor.UTF8DecodeInvalid, DupMapKey: cbor.DupMapKeyQuiet,
	}.DecMode()
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewSource(3))
	valid, invalid := 0, 0
	for range 20000 {
		doc, err := cbor.Marshal(randomValue(r, 0))
		if err != nil {
			t.Fatal(err)
		}
		switch r.Intn(3) {
		case 1:
			doc[r.Intn(len(doc))] = byte(r.Intn(256))
		case 2:
			doc = doc[:r.Intn(len(doc)+1)]
		}
		want := dm.Wellformed(doc) == nil
		if got := WellFormed(doc); got != want {
			t.Fatalf("% x: WellFormed is %v, fxamacker says %v", doc, got, want)
		}
		if want {
			valid++
		} else {
			invalid++
		}
	}
	if valid < 5000 || invalid < 5000 {
		t.Errorf("unbalanced sample : %d well-formed, %d not", valid, invalid)
	}
}

func randomValue(r *rand.Rand, depth int) any {
	n := r.Intn(10)
	if depth > 4 {
		n %= 6
	}
	switch n {
	case 0:
		return r.Int63() - r.Int63()
	case 1:
		return r.Float64()
	case 2:
		return "text" + string(rune('a'+r.Intn(26)))
	case 3:
		return []byte{byte(r.Intn(256)), 1, 2}
	case 4:
		return nil
	case 5:
		return cbor.Tag{Number: uint64(r.Intn(1000)) + 6, Content: r.Intn(100)}
	case 6, 7:
		a := make([]any, r.Intn(4))
		for i := range a {
			a[i] = randomValue(r, depth+1)
		}
		return a
	}
	m := map[any]any{}
	for i := range r.Intn(4) {
		m[i] = randomValue(r, depth+1)
	}
	return m
}

func shortHex(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}

func TestScan_ReportsStructures(t *testing.T) {
	for _, c := range []struct {
		hex        string
		structures bool
	}{
		{"8301a1020304", false},
		{"81d9dfff8319e00081616101", true}, // a definition, nested
		{"9fd9e0008102ff", true},           // a reference
		{"d9fffe8100", true},               // the last id
		{"d9dffe8100", false},              // tag 0xdffe, another cbor-x record form, is not one
		{"d9ffff8100", false},              // past the ids
	} {
		ok, got := Scan(mustDecodeHex(t, c.hex))
		if !ok || got != c.structures {
			t.Errorf("%s : well-formed %v, structures %v", c.hex, ok, got)
		}
	}
}

func mustDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
