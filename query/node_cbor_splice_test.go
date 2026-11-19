package query

import (
	"bytes"
	"encoding/hex"
	"math/rand"
	"testing"
)

// reachable is every node of the tree under c, c included, through elements, keys, values and tag contents.
func reachable(c *cnode) []*cnode {
	out := []*cnode{c}
	switch c.item {
	case itArray, itMap, itOpaqueMap:
		n := int(c.b)
		if c.item != itArray {
			n *= 2
		}
		for i := range n {
			out = append(out, reachable(c.kid(c.a+i))...)
		}
	case itBadTag:
		out = append(out, reachable(&c.doc.nodes[c.a])...)
	}
	return out
}

// checkSplice checks every node of a document : its spliced bytes read on their own, hold no structure, and give
// the same tree as the node.
func checkSplice(t *testing.T, name string, data []byte) {
	t.Helper()
	root, err := NewCBORNode(data)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	for _, c := range reachable(root.(*cnode)) {
		spliced := c.appendCBOR(nil)
		again, err := NewCBORNode(spliced)
		if err != nil {
			t.Errorf("%s: % x reads %v", name, spliced, err)
			continue
		}
		if s := again.(*cnode).doc.structures; len(s) > 0 {
			t.Errorf("%s: % x still holds %d structures", name, spliced, len(s))
		}
		if d := compareNodes("$", again, c); d != "" {
			t.Errorf("%s: % x: %s", name, spliced, d)
		}
	}
}

// Without a structure, every item is its own bytes : random documents, and the mutated ones that still read.
func TestCBORSplice_WithoutStructuresIsTheBytes(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	checked := 0
	for range 3000 {
		doc := cborBytes(t, randomValue(r, 0))
		for m := range 4 {
			data := doc
			if m > 0 {
				data = append([]byte{}, doc...)
				data[r.Intn(len(data))] = byte(r.Intn(256))
			}
			root, err := NewCBORNode(data)
			if err != nil || len(root.(*cnode).doc.structures) > 0 {
				continue
			}
			for _, c := range reachable(root.(*cnode)) {
				if got := c.appendCBOR(nil); !bytes.Equal(got, data[c.start:c.end]) {
					t.Fatalf("% x: an item spliced to % x, not its bytes", data, got)
				}
				if _, err := NewCBORNode(data[c.start:c.end]); err != nil {
					t.Fatalf("% x: the bytes % x of an item do not read alone : %v", data, data[c.start:c.end], err)
				}
			}
			if !bytes.Equal(root.(*cnode).appendCBOR(nil), data) {
				t.Fatalf("% x: the document is not its own bytes", data)
			}
			checked++
		}
	}
	if checked < 5000 {
		t.Errorf("only %d documents checked", checked)
	}
}

// What the client wrote stays as it was around and inside the structures : lengths, indefinite containers,
// undefined, tags.
func TestCBORSplice_RewritesOnlyTheStructures(t *testing.T) {
	const (
		def0 = "d9dfff84" + "19e000" + "82626964646e616d65" // id 0 : ["id", "name"], then 2 values
		ref0 = "d9e00082"
	)
	// the map a structure of id 0 becomes
	map0 := func(id, name string) string { return "a2" + "626964" + id + "646e616d65" + name }
	for name, c := range map[string]struct{ doc, want string }{
		"a definition and a reference": {"82" + def0 + "01" + "f7" + ref0 + "1802" + "f6",
			"82" + map0("01", "f7") + map0("1802", "f6")},
		"in an indefinite array, next to a long integer": {"9f" + "1b0000000000000001" + def0 + "01" + "f6" + ref0 + "02" + "f5" + "ff",
			"9f" + "1b0000000000000001" + map0("01", "f6") + map0("02", "f5") + "ff"},
		"in an indefinite map": {"bf" + "6161" + def0 + "01" + "f6" + "ff", "bf" + "6161" + map0("01", "f6") + "ff"},
		"in an unknown tag":    {"d9270f" + def0 + "01" + "f6", "d9270f" + map0("01", "f6")},
		"as the key of a map":  {"a1" + def0 + "01" + "f6" + "01", "a1" + map0("01", "f6") + "01"},
		"nested":               {"d9dfff83" + "19e001" + "816161" + def0 + "01" + "f6", "a1" + "6161" + map0("01", "f6")},
		"redefined":            {"82" + def0 + "01" + "f6" + "d9dfff83" + "19e000" + "816178" + "f5", "82" + map0("01", "f6") + "a1" + "6178" + "f5"},
	} {
		data, err := hex.DecodeString(c.doc)
		if err != nil {
			t.Fatal(err)
		}
		root, err := NewCBORNode(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := hex.EncodeToString(root.(*cnode).appendCBOR(nil)); got != c.want {
			t.Errorf("%s: spliced to %s, want %s", name, got, c.want)
		}
		checkSplice(t, name, data)
	}
}

// A reference whose definition sits outside the item still splices : the names come from the definition.
func TestCBORSplice_ReferenceToADefinitionOutside(t *testing.T) {
	data, _ := hex.DecodeString("82" + "d9dfff83" + "19e000" + "816161" + "01" + "a1" + "6176" + "d9e00081" + "02")
	root, err := NewCBORNode(data)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := root.(*cnode).kid(root.(*cnode).a + 1).ObjectGet("v")
	if got := hex.EncodeToString(v.(*cnode).appendCBOR(nil)); got != "a1616102" {
		t.Errorf("spliced to %s, want a1616102", got)
	}
}

func TestSpliceCBOR_ParsesOnlyWithStructures(t *testing.T) {
	plain := []byte{0x82, 0x01, 0x02}
	if got, err := SpliceCBOR(plain); err != nil || &got[0] != &plain[0] {
		t.Errorf("an item without structures is not returned as is : % x, %v", got, err)
	}
	withStructures := []byte{0x82, 0xd9, 0xdf, 0xff, 0x83, 0x19, 0xe0, 0x00, 0x81, 0x61, 0x61, 0x01, 0xd9, 0xe0, 0x00, 0x81, 0x02}
	want := []byte{0x82, 0xa1, 0x61, 0x61, 0x01, 0xa1, 0x61, 0x61, 0x02}
	if got, err := SpliceCBOR(withStructures); err != nil || !bytes.Equal(got, want) {
		t.Errorf("spliced to % x (%v), want % x", got, err, want)
	}
	for _, bad := range [][]byte{{0x82, 0x01}, {0x82, 0xd9, 0xe0, 0x05, 0x81, 0x01, 0x01}} { // truncated ; an undefined reference
		if _, err := SpliceCBOR(bad); err == nil {
			t.Errorf("% x is accepted", bad)
		}
	}
}
