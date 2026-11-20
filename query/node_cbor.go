package query

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rel-server/rel/cborenc"
	"github.com/samber/oops"
)

// The CBOR adapter : a Node over a CBOR document, read in one pass into a flat arena of item positions in the
// request body ; a string or number is only built when a column asks for it. The pass validates the whole
// document, so a malformed body is refused before anything else happens.

const (
	cborMaxDepth = 64 // nesting of arrays, maps and tags
)

// NewCBORNode reads one CBOR data item, which must be all of data. The node refers to data, which the
// caller must not change while the node is in use.
func NewCBORNode(data []byte) (Node, error) {
	if len(data) > math.MaxUint32 {
		return nil, oops.Errorf("query: invalid CBOR: a document of more than 4 GiB")
	}
	doc := &cborDoc{data: data, nodes: make([]cnode, 0, len(data)/8+8)}
	d := cborDecoder{doc: doc}
	idx, err := d.item(0)
	if err != nil {
		return nil, oops.With("offset", d.pos).Wrapf(err, "query: invalid CBOR")
	}
	if d.pos != len(data) {
		return nil, oops.With("offset", d.pos).Errorf("query: invalid CBOR: %d bytes of extraneous data after the item", len(data)-d.pos)
	}
	return &doc.nodes[idx], nil
}

// SpliceCBOR returns data, one CBOR item, as a CBOR column receives it : data itself when it is well-formed and holds
// no structure, a spliced copy otherwise. Only an item with structures is read, and checked, as a request is.
func SpliceCBOR(data []byte) ([]byte, error) {
	switch ok, structures := cborenc.Scan(data); {
	case !ok:
		return nil, oops.Errorf("query: invalid CBOR: not one well-formed item")
	case !structures:
		return data, nil
	}
	n, err := NewCBORNode(data)
	if err != nil {
		return nil, err
	}
	c := n.(*cnode)
	if len(c.doc.structures) == 0 {
		return data, nil
	}
	return c.appendCBOR(nil), nil
}

type cborItem uint8

const (
	itNull cborItem = iota
	itFalse
	itTrue
	itInt     // extra holds the int64
	itUint    // extra holds an unsigned value above the int64 range
	itNegBig  // extra holds n of -1-n, below the int64 range
	itFloat   // extra holds the float64 bits
	itNumText // a number kept as text : b 0 a bignum (strs[a] holds its digits), b 1 a decimal fraction (strs[a] its mantissa, extra its exponent)
	itText    // a, b locate the bytes in data ; or, when chunked, a is an index in blobs
	itBytes   // like itText
	itArray   // a is the index in kids of the first element, b the count
	itMap     // a is the index in kids of the first key, b the count of pairs (keys and values alternate)
	itInstant // extra holds an index in times (tags 0 and 1)
	itDate    // extra holds the days since 1970-01-01 (tag 100)
	itUUID    // like itBytes, 16 bytes (tag 37)
	itBadTag  // a tag this adapter has no meaning for, or one with the wrong content : extra holds its number, a its content node
	// Only written to a CBOR column, as they are : no Node kind covers them.
	itOpaqueMap // a map with a key that is not text : like itMap
	itSimple    // a simple value with no meaning here : extra holds it
)

// cnode is one item of the document. Its fields are ordered, and b is an int32, so that start and end fit in the
// 40 bytes it took without them.
type cnode struct {
	doc        *cborDoc
	a          int
	extra      uint64
	start, end uint32 // the item's bytes in data, its head included
	b          int32
	item       cborItem
	chunked    bool // an indefinite-length string or byte string, assembled in blobs
}

type cborDoc struct {
	data  []byte
	nodes []cnode
	kids  []int32 // the elements of arrays and the keys and values of maps, by index in nodes
	strs  []string
	blobs [][]byte
	times []time.Time
	// the offset of each structure's tag, increasing : a document without any is written as it is
	structures []uint32
}

// ---- reading ------------------------------------------------------------------------

type cborDecoder struct {
	doc     *cborDoc
	pos     int
	structs map[int][]int32 // the names of each structure defined so far, by id : indexes of their text nodes
}

func (d *cborDecoder) remaining() int { return len(d.doc.data) - d.pos }

func (d *cborDecoder) add(n cnode) int32 {
	n.doc = d.doc
	d.doc.nodes = append(d.doc.nodes, n)
	return int32(len(d.doc.nodes) - 1)
}

// head reads an item's initial byte and its argument. indef is set for an indefinite length (ai 31).
func (d *cborDecoder) head() (major, ai byte, val uint64, indef bool, err error) {
	if d.remaining() < 1 {
		return 0, 0, 0, false, oops.Errorf("unexpected end of data")
	}
	b := d.doc.data[d.pos]
	d.pos++
	major, ai = b>>5, b&0x1f
	switch {
	case ai < 24:
		return major, ai, uint64(ai), false, nil
	case ai == 24 || ai == 25 || ai == 26 || ai == 27:
		size := 1 << (ai - 24)
		if d.remaining() < size {
			return 0, 0, 0, false, oops.Errorf("unexpected end of data")
		}
		raw := d.doc.data[d.pos : d.pos+size]
		d.pos += size
		switch size {
		case 1:
			val = uint64(raw[0])
		case 2:
			val = uint64(binary.BigEndian.Uint16(raw))
		case 4:
			val = uint64(binary.BigEndian.Uint32(raw))
		default:
			val = binary.BigEndian.Uint64(raw)
		}
		return major, ai, val, false, nil
	case ai == 31:
		return major, ai, 0, true, nil
	}
	return 0, 0, 0, false, oops.Errorf("reserved additional information %d", ai)
}

// item reads one data item and returns its index in the arena.
func (d *cborDecoder) item(depth int) (int32, error) {
	start := d.pos
	idx, err := d.itemAt(depth)
	if err != nil {
		return 0, err
	}
	n := &d.doc.nodes[idx]
	n.start, n.end = uint32(start), uint32(d.pos)
	return idx, nil
}

func (d *cborDecoder) itemAt(depth int) (int32, error) {
	start := d.pos
	major, ai, val, indef, err := d.head()
	if err != nil {
		return 0, err
	}
	switch major {
	case 0:
		if indef {
			return 0, oops.Errorf("an integer cannot have an indefinite length")
		}
		if val > math.MaxInt64 {
			return d.add(cnode{item: itUint, extra: val}), nil
		}
		return d.add(cnode{item: itInt, extra: val}), nil
	case 1:
		if indef {
			return 0, oops.Errorf("an integer cannot have an indefinite length")
		}
		if val > math.MaxInt64 {
			return d.add(cnode{item: itNegBig, extra: val}), nil
		}
		return d.add(cnode{item: itInt, extra: uint64(-1 - int64(val))}), nil
	case 2, 3:
		return d.stringItem(major, val, indef)
	case 4:
		return d.arrayItem(val, indef, depth)
	case 5:
		return d.mapItem(val, indef, depth)
	case 6:
		if indef {
			return 0, oops.Errorf("a tag cannot have an indefinite length")
		}
		if isStructureTag(val) {
			d.doc.structures = append(d.doc.structures, uint32(start))
			return d.structureItem(val, depth)
		}
		return d.tagItem(val, depth)
	}
	return d.simpleItem(ai, val, indef)
}

func (d *cborDecoder) simpleItem(ai byte, val uint64, indef bool) (int32, error) {
	switch {
	case indef:
		return 0, oops.Errorf("unexpected break")
	case ai == 20:
		return d.add(cnode{item: itFalse}), nil
	case ai == 21:
		return d.add(cnode{item: itTrue}), nil
	case ai == 22:
		return d.add(cnode{item: itNull}), nil
	case ai == 23: // undefined : null, except that a map member holding it is absent, as JSON drops it
		return d.add(cnode{item: itNull, extra: cborUndefined}), nil
	case ai == 25:
		return d.add(cnode{item: itFloat, extra: math.Float64bits(float16ToFloat64(uint16(val)))}), nil
	case ai == 26:
		return d.add(cnode{item: itFloat, extra: math.Float64bits(float64(math.Float32frombits(uint32(val))))}), nil
	case ai == 27:
		return d.add(cnode{item: itFloat, extra: val}), nil
	case ai == 24 && val < 32:
		return 0, oops.Errorf("a two-byte simple value below 32")
	}
	return d.add(cnode{item: itSimple, extra: val}), nil
}

// float16ToFloat64 widens an IEEE 754 half precision float.
func float16ToFloat64(h uint16) float64 {
	sign := 1.0
	if h&0x8000 != 0 {
		sign = -1
	}
	exp, frac := int(h>>10)&0x1f, int(h&0x3ff)
	switch exp {
	case 0:
		return sign * math.Ldexp(float64(frac), -24)
	case 31:
		if frac == 0 {
			return sign * math.Inf(1)
		}
		return math.NaN()
	}
	return sign * math.Ldexp(float64(frac|0x400), exp-25)
}

func (d *cborDecoder) stringItem(major byte, length uint64, indef bool) (int32, error) {
	kind := itBytes
	if major == 3 {
		kind = itText
	}
	if !indef {
		if length > uint64(d.remaining()) {
			return 0, oops.Errorf("a string of %d bytes runs past the end of the data", length)
		}
		off := d.pos
		d.pos += int(length)
		if kind == itText && !utf8.Valid(d.doc.data[off:d.pos]) {
			return 0, oops.Errorf("a text string that is not valid UTF-8")
		}
		return d.add(cnode{item: kind, a: off, b: int32(length)}), nil
	}
	// chunks of the same major type, each a definite-length string, up to a break
	var assembled []byte
	for {
		if d.remaining() < 1 {
			return 0, oops.Errorf("unexpected end of data in an indefinite-length string")
		}
		if d.doc.data[d.pos] == 0xff {
			d.pos++
			break
		}
		m, _, n, ind, err := d.head()
		if err != nil {
			return 0, err
		}
		if m != major || ind {
			return 0, oops.Errorf("a chunk of an indefinite-length string that is not a definite-length string of the same type")
		}
		if n > uint64(d.remaining()) {
			return 0, oops.Errorf("a chunk of %d bytes runs past the end of the data", n)
		}
		chunk := d.doc.data[d.pos : d.pos+int(n)]
		d.pos += int(n)
		if kind == itText && !utf8.Valid(chunk) {
			return 0, oops.Errorf("a text chunk that is not valid UTF-8")
		}
		assembled = append(assembled, chunk...)
	}
	d.doc.blobs = append(d.doc.blobs, assembled)
	return d.add(cnode{item: kind, chunked: true, a: len(d.doc.blobs) - 1, b: int32(len(assembled))}), nil
}

// countFits checks that a container header's count can be backed by the bytes left : every element takes at least one.
func (d *cborDecoder) countFits(n uint64, perElement int) error {
	if n > uint64(d.remaining())/uint64(perElement) {
		return oops.Errorf("a count of %d runs past the end of the data", n)
	}
	return nil
}

func (d *cborDecoder) arrayItem(n uint64, indef bool, depth int) (int32, error) {
	if depth >= cborMaxDepth {
		return 0, oops.Errorf("nested more than %d levels", cborMaxDepth)
	}
	if indef {
		var elems []int32
		for {
			if d.remaining() < 1 {
				return 0, oops.Errorf("unexpected end of data in an indefinite-length array")
			}
			if d.doc.data[d.pos] == 0xff {
				d.pos++
				break
			}
			e, err := d.item(depth + 1)
			if err != nil {
				return 0, err
			}
			elems = append(elems, e)
		}
		start := len(d.doc.kids)
		d.doc.kids = append(d.doc.kids, elems...)
		return d.add(cnode{item: itArray, a: start, b: int32(len(elems))}), nil
	}
	if err := d.countFits(n, 1); err != nil {
		return 0, err
	}
	start := len(d.doc.kids)
	d.doc.kids = append(d.doc.kids, make([]int32, n)...)
	for i := 0; i < int(n); i++ {
		e, err := d.item(depth + 1)
		if err != nil {
			return 0, err
		}
		d.doc.kids[start+i] = e
	}
	return d.add(cnode{item: itArray, a: start, b: int32(n)}), nil
}

func (d *cborDecoder) mapItem(n uint64, indef bool, depth int) (int32, error) {
	if depth >= cborMaxDepth {
		return 0, oops.Errorf("nested more than %d levels", cborMaxDepth)
	}
	var kids []int32 // used for an indefinite map, whose size is not known up front
	kind := itMap
	start := len(d.doc.kids)
	if !indef {
		if err := d.countFits(n, 2); err != nil {
			return 0, err
		}
		d.doc.kids = append(d.doc.kids, make([]int32, 2*n)...)
	}
	count := 0
	for ; indef || count < int(n); count++ {
		if indef {
			if d.remaining() < 1 {
				return 0, oops.Errorf("unexpected end of data in an indefinite-length map")
			}
			if d.doc.data[d.pos] == 0xff {
				d.pos++
				break
			}
		}
		k, err := d.item(depth + 1)
		if err != nil {
			return 0, err
		}
		if d.doc.nodes[k].item != itText {
			kind = itOpaqueMap
		}
		v, err := d.item(depth + 1)
		if err != nil {
			return 0, err
		}
		if indef {
			kids = append(kids, k, v)
		} else {
			d.doc.kids[start+2*count], d.doc.kids[start+2*count+1] = k, v
		}
	}
	if indef {
		start = len(d.doc.kids)
		d.doc.kids = append(d.doc.kids, kids...)
	}
	if err := d.checkDistinctKeys(start, count); err != nil {
		return 0, err
	}
	return d.add(cnode{item: kind, a: start, b: int32(count)}), nil
}

// checkDistinctKeys compares text keys by their text, and any other key by its bytes : the same integer written
// in two lengths is not seen as a repeat.
func (d *cborDecoder) checkDistinctKeys(start, pairs int) error {
	keyNode := func(i int) *cnode { return &d.doc.nodes[d.doc.kids[start+2*i]] }
	if pairs <= 8 {
		for i := 1; i < pairs; i++ {
			for j := 0; j < i; j++ {
				if x, y := keyNode(i), keyNode(j); (x.item == itText) == (y.item == itText) && string(x.keyBytes()) == string(y.keyBytes()) {
					return oops.Errorf("duplicate map key %q", x.keyBytes())
				}
			}
		}
		return nil
	}
	seen := make(map[string]struct{}, pairs)
	for i := 0; i < pairs; i++ {
		k := keyNode(i)
		key := string(k.keyBytes())
		if k.item != itText {
			key = "\xff" + key // never equal to a text key : valid UTF-8 has no 0xff byte
		}
		if _, dup := seen[key]; dup {
			return oops.Errorf("duplicate map key %q", k.keyBytes())
		}
		seen[key] = struct{}{}
	}
	return nil
}

// keyBytes is what tells a map key from another : the text of a text key, the bytes of any other.
func (c *cnode) keyBytes() []byte {
	if c.item == itText {
		return c.textBytes()
	}
	return c.doc.data[c.start:c.end]
}

func isStructureTag(tag uint64) bool {
	return tag == cborenc.TagStructureDef || (tag >= cborenc.StructureTagBase && tag < cborenc.StructureTagBase+cborenc.MaxStructures)
}

// tagItem reads a tag and its content, and gives the pair the meaning this adapter knows for it.
func (d *cborDecoder) tagItem(tag uint64, depth int) (int32, error) {
	if depth >= cborMaxDepth {
		return 0, oops.Errorf("nested more than %d levels", cborMaxDepth)
	}
	c, err := d.item(depth + 1)
	if err != nil {
		return 0, err
	}
	content := d.doc.nodes[c]
	bad := func() (int32, error) { return d.add(cnode{item: itBadTag, a: int(c), extra: tag}), nil }

	switch tag {
	case 2, 3: // a bignum : the bytes of an unsigned integer, big endian
		if content.item != itBytes {
			return bad()
		}
		n := new(big.Int).SetBytes(content.bytes())
		if tag == 3 {
			n.Neg(n.Add(n, big.NewInt(1)))
		}
		return d.addText(itNumText, n.String(), 0, 0), nil
	case 0:
		if content.item != itText {
			return bad()
		}
		t, err := time.Parse(time.RFC3339Nano, string(content.textBytes()))
		if err != nil {
			return 0, oops.Wrapf(err, "a tag 0 value that is not an RFC 3339 date and time")
		}
		return d.addInstant(t), nil
	case 1:
		var t time.Time
		switch content.item {
		case itInt:
			t = time.Unix(int64(content.extra), 0)
		case itUint:
			return 0, oops.Errorf("a tag 1 value out of range")
		case itFloat:
			f := math.Float64frombits(content.extra)
			if math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > 9e18 { // beyond what a time.Time can hold
				return 0, oops.Errorf("a tag 1 value out of range")
			}
			sec := math.Floor(f)
			t = time.Unix(int64(sec), int64(math.Round((f-sec)*1e9)))
		default:
			return bad()
		}
		return d.addInstant(t), nil
	case 4: // a decimal fraction, [exponent, mantissa]
		if content.item != itArray || content.b != 2 {
			return bad()
		}
		kids := d.doc.kids[content.a : content.a+2]
		exp, mant := d.doc.nodes[kids[0]], d.doc.nodes[kids[1]]
		if exp.item != itInt {
			return bad()
		}
		text, ok := mant.integerText()
		if !ok {
			return bad()
		}
		return d.addText(itNumText, text, 1, exp.extra), nil
	case 37:
		if content.item != itBytes || content.b != 16 {
			return bad()
		}
		return d.add(cnode{item: itUUID, chunked: content.chunked, a: content.a, b: content.b}), nil
	case 100:
		if content.item != itInt {
			return bad()
		}
		return d.add(cnode{item: itDate, extra: content.extra}), nil
	}
	return bad()
}

// structureItem reads a structure's definition or reference as the map it stands for :
// its names are the text nodes of the definition, shared by every record that refers to it.
func (d *cborDecoder) structureItem(tag uint64, depth int) (int32, error) {
	bad := func(format string, args ...any) (int32, error) {
		return 0, oops.Errorf("tag %d: "+format, append([]any{tag}, args...)...)
	}
	// the tag and its array each count as a level, so the values sit two below the tag
	if depth+1 >= cborMaxDepth {
		return 0, oops.Errorf("nested more than %d levels", cborMaxDepth)
	}
	major, _, n, indef, err := d.head()
	if err != nil {
		return 0, err
	}
	if major != 4 || indef {
		return bad("a structure is a definite-length array")
	}
	if err := d.countFits(n, 1); err != nil {
		return 0, err
	}

	var names []int32
	if tag == cborenc.TagStructureDef {
		if n < 2 {
			return bad("a definition holds an id, the names and the values")
		}
		idTag, err := d.item(depth + 2)
		if err != nil {
			return 0, err
		}
		id := d.doc.nodes[idTag]
		if id.item != itInt || int64(id.extra) < cborenc.StructureTagBase || int64(id.extra) >= cborenc.StructureTagBase+cborenc.MaxStructures {
			return bad("the id of a definition is an integer from %d to %d", cborenc.StructureTagBase, cborenc.StructureTagBase+cborenc.MaxStructures-1)
		}
		if names, err = d.structureNames(tag, depth+2); err != nil {
			return 0, err
		}
		if int(n) != len(names)+2 {
			return bad("a definition of %d names is an array of %d items, not %d", len(names), len(names)+2, n)
		}
		if d.structs == nil {
			d.structs = map[int][]int32{}
		}
		d.structs[int(id.extra)-cborenc.StructureTagBase] = names
	} else {
		var ok bool
		if names, ok = d.structs[int(tag)-cborenc.StructureTagBase]; !ok {
			return bad("a structure that was not defined")
		}
		if int(n) != len(names) {
			return bad("a structure of %d names holds %d values", len(names), n)
		}
	}

	start := len(d.doc.kids)
	d.doc.kids = append(d.doc.kids, make([]int32, 2*len(names))...)
	for i, k := range names {
		v, err := d.item(depth + 2)
		if err != nil {
			return 0, err
		}
		d.doc.kids[start+2*i], d.doc.kids[start+2*i+1] = k, v
	}
	return d.add(cnode{item: itMap, a: start, b: int32(len(names))}), nil
}

// structureNames reads the array of names of a definition : text strings, all different.
func (d *cborDecoder) structureNames(tag uint64, depth int) ([]int32, error) {
	major, _, n, indef, err := d.head()
	if err != nil {
		return nil, err
	}
	if major != 4 || indef {
		return nil, oops.Errorf("tag %d: the names of a definition are a definite-length array", tag)
	}
	if err := d.countFits(n, 1); err != nil {
		return nil, err
	}
	names := make([]int32, n)
	for i := range names {
		k, err := d.item(depth + 1)
		if err != nil {
			return nil, err
		}
		if d.doc.nodes[k].item != itText {
			return nil, oops.Errorf("tag %d: a name that is not a text string", tag)
		}
		names[i] = k
	}
	// checked once here, not by every record that refers to the definition
	seen := make(map[string]struct{}, len(names))
	for _, k := range names {
		name := string(d.doc.nodes[k].textBytes())
		if _, dup := seen[name]; dup {
			return nil, oops.Errorf("tag %d: duplicate name %q", tag, name)
		}
		seen[name] = struct{}{}
	}
	return names, nil
}

func (d *cborDecoder) addText(item cborItem, text string, b int32, extra uint64) int32 {
	d.doc.strs = append(d.doc.strs, text)
	return d.add(cnode{item: item, a: len(d.doc.strs) - 1, b: b, extra: extra})
}

func (d *cborDecoder) addInstant(t time.Time) int32 {
	d.doc.times = append(d.doc.times, t)
	return d.add(cnode{item: itInstant, extra: uint64(len(d.doc.times) - 1)})
}

// ---- the Node ------------------------------------------------------------------------

func (c *cnode) bytes() []byte {
	if c.chunked {
		return c.doc.blobs[c.a]
	}
	return c.doc.data[c.a : c.a+int(c.b)]
}

func (c *cnode) textBytes() []byte { return c.bytes() }

func (c *cnode) Kind() NodeKind {
	switch c.item {
	case itNull:
		return KindNull
	case itFalse, itTrue:
		return KindBool
	case itInt, itUint, itNegBig, itFloat, itNumText:
		return KindNumber
	case itText:
		return KindString
	case itBytes:
		return KindBytes
	case itArray:
		return KindArray
	case itMap:
		return KindObject
	case itInstant:
		return KindInstant
	case itDate:
		return KindDate
	case itUUID:
		return KindUUID
	}
	return KindInvalid
}

// unsupported describes a value no Node kind covers, for an error message.
func (c *cnode) unsupported() string {
	switch c.item {
	case itOpaqueMap:
		return "a CBOR map with a key that is not text"
	case itSimple:
		return fmt.Sprintf("CBOR simple value %d", c.extra)
	}
	return fmt.Sprintf("CBOR tag %d with this content", c.extra)
}

// appendCBOR appends the item as the client wrote it, except that each structure in it is written as the map it
// stands for, so that the bytes need no definition from elsewhere in the document.
func (c *cnode) appendCBOR(dst []byte) []byte {
	data := c.doc.data
	if !c.doc.structureWithin(c.start, c.end) {
		return append(dst, data[c.start:c.end]...)
	}
	switch c.item {
	case itArray, itMap, itOpaqueMap:
		n := int(c.b)
		if c.item != itArray {
			n *= 2
		}
		if data[c.start]>>5 == 6 { // a structure : a map of its names and values
			dst = cborenc.AppendHead(dst, 5, uint64(c.b))
			for i := range n {
				dst = c.kid(c.a + i).appendCBOR(dst)
			}
			return dst
		}
		// a container holds an item, so it has a first one ; its head, definite or not, is the bytes before it
		dst = append(dst, data[c.start:c.kid(c.a).start]...)
		for i := range n {
			dst = c.kid(c.a + i).appendCBOR(dst)
		}
		if data[c.start]&0x1f == 31 {
			dst = append(dst, cborenc.Break)
		}
		return dst
	case itBadTag:
		content := &c.doc.nodes[c.a]
		return content.appendCBOR(append(dst, data[c.start:content.start]...))
	}
	// no other item holds an item that can be a structure
	return append(dst, data[c.start:c.end]...)
}

// structureWithin reports whether a structure's tag starts in data[start:end].
func (doc *cborDoc) structureWithin(start, end uint32) bool {
	i, _ := slices.BinarySearch(doc.structures, start)
	return i < len(doc.structures) && doc.structures[i] < end
}

// integerText is the digits of an integer item.
func (c *cnode) integerText() (string, bool) {
	switch c.item {
	case itInt:
		return strconv.FormatInt(int64(c.extra), 10), true
	case itUint:
		return strconv.FormatUint(c.extra, 10), true
	case itNegBig:
		n := new(big.Int).SetUint64(c.extra)
		return n.Neg(n.Add(n, big.NewInt(1))).String(), true
	case itNumText:
		if c.b == 0 {
			return c.doc.strs[c.a], true
		}
	}
	return "", false
}

// floatText prints f so it never reads as an integer : 3 is "3.0".
func floatText(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

// numberText is the text of a number node.
func (c *cnode) numberText() (string, bool) {
	switch c.item {
	case itFloat:
		return floatText(math.Float64frombits(c.extra)), true
	case itNumText:
		if c.b == 1 {
			return c.doc.strs[c.a] + "e" + strconv.FormatInt(int64(c.extra), 10), true
		}
	}
	return c.integerText()
}

// decimalText is a number node as plain decimal digits, without an exponent where the exponent is
// small, which is what a type that reads numbers from text (money, a range bound) expects.
func (c *cnode) decimalText() (string, bool) {
	switch c.item {
	case itFloat:
		f := math.Float64frombits(c.extra)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return floatText(f), true
		}
		return strconv.FormatFloat(f, 'f', -1, 64), true
	case itNumText:
		if c.b == 1 {
			return plainDecimal(c.doc.strs[c.a], int64(c.extra)), true
		}
	}
	return c.integerText()
}

// plainDecimal writes mantissa * 10^exp without an exponent when |exp| is at most 64.
func plainDecimal(mantissa string, exp int64) string {
	if exp == 0 || exp > 64 || exp < -64 {
		if exp == 0 {
			return mantissa
		}
		return mantissa + "e" + strconv.FormatInt(exp, 10)
	}
	neg := strings.HasPrefix(mantissa, "-")
	digits := strings.TrimPrefix(mantissa, "-")
	var out string
	if exp > 0 {
		out = digits + strings.Repeat("0", int(exp))
	} else {
		point := len(digits) + int(exp)
		if point > 0 {
			out = digits[:point] + "." + digits[point:]
		} else {
			out = "0." + strings.Repeat("0", -point) + digits
		}
	}
	if neg {
		return "-" + out
	}
	return out
}

func (c *cnode) StrictString() (string, error) {
	if c.item == itText {
		return string(c.bytes()), nil
	}
	return "", errNotString
}

func (c *cnode) StrictBool() (bool, error) {
	switch c.item {
	case itTrue:
		return true, nil
	case itFalse:
		return false, nil
	}
	return false, nodeError("value is not a boolean")
}

func (c *cnode) StrictInt64() (int64, error) {
	switch c.item {
	case itInt:
		return int64(c.extra), nil
	case itNumText:
		if c.b == 0 {
			if i, err := strconv.ParseInt(c.doc.strs[c.a], 10, 64); err == nil {
				return i, nil
			}
		}
	}
	return 0, nodeError("value is not an integer that fits 64 bits")
}

func (c *cnode) StrictFloat64() (float64, error) {
	if c.item == itFloat {
		return math.Float64frombits(c.extra), nil
	}
	if c.Kind() != KindNumber {
		return 0, nodeError("value is not a number")
	}
	text, _ := c.numberText()
	return strconv.ParseFloat(text, 64)
}

func (c *cnode) kid(i int) *cnode { return &c.doc.nodes[c.doc.kids[i]] }

// cborUndefined is the extra of the itNull node decoded from undefined.
const cborUndefined = 1

func (c *cnode) undefined() bool { return c.item == itNull && c.extra == cborUndefined }

func (c *cnode) ArrayItems() ([]Node, error) {
	if c.item != itArray {
		return nil, errNotArray
	}
	out := make([]Node, int(c.b))
	for i := range out {
		out[i] = c.kid(c.a + i)
	}
	return out, nil
}

func (c *cnode) ObjectGet(key string) (Node, bool) {
	if c.item != itMap {
		return nil, false
	}
	for i := 0; i < int(c.b); i++ {
		if string(c.kid(c.a+2*i).bytes()) == key {
			v := c.kid(c.a + 2*i + 1)
			return v, !v.undefined()
		}
	}
	return nil, false
}

func (c *cnode) ObjectEach(fn func(string, Node) error) error {
	if c.item != itMap {
		return errNotObject
	}
	for i := 0; i < int(c.b); i++ {
		if v := c.kid(c.a + 2*i + 1); !v.undefined() {
			if err := fn(string(c.kid(c.a+2*i).bytes()), v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *cnode) StrictBytes() ([]byte, error) {
	if c.item == itBytes || c.item == itUUID {
		return c.bytes(), nil
	}
	return nil, nodeError("value is not a byte string")
}

func (c *cnode) Instant() (time.Time, error) {
	if c.item == itInstant {
		return c.doc.times[c.extra], nil
	}
	return time.Time{}, nodeError("value is not a date and time")
}

func (c *cnode) Days() (int64, error) {
	if c.item == itDate {
		return int64(c.extra), nil
	}
	return 0, nodeError("value is not an epoch-based date")
}

// Raw returns the node as JSON text, which is what a json or jsonb column receives : a number
// keeps its digits (a non-finite float reads as NaN, Infinity or -Infinity, which the number
// encoders accept and jsonText refuses).
func (c *cnode) Raw() (string, error) {
	if c.Kind() == KindNumber {
		text, _ := c.numberText()
		return text, nil
	}
	return c.jsonText()
}

// jsonText converts the node to JSON : maps with text keys become objects (keys sorted), arrays arrays,
// text, integers, booleans and null map directly, finite floats and tag 4 become numbers. A byte
// string, a date or time, any other tag, and a non-finite float have no JSON form.
func (c *cnode) jsonText() (string, error) {
	var b strings.Builder
	if err := c.writeJSON(&b); err != nil {
		return "", err
	}
	return b.String(), nil
}

func errNoJSONForm(what string) error {
	return errBadValue("json", "", what+" cannot be written as JSON")
}

func (c *cnode) writeJSON(b *strings.Builder) error {
	switch c.item {
	case itNull:
		b.WriteString("null")
	case itTrue:
		b.WriteString("true")
	case itFalse:
		b.WriteString("false")
	case itText:
		enc, err := json.Marshal(string(c.bytes()))
		if err != nil {
			return errNoJSONForm("a string that is not valid UTF-8")
		}
		b.Write(enc)
	case itFloat:
		f := math.Float64frombits(c.extra)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return errNoJSONForm("a non-finite number")
		}
		b.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
	case itInt, itUint, itNegBig, itNumText:
		text, _ := c.numberText()
		b.WriteString(text)
	case itArray:
		b.WriteByte('[')
		for i := 0; i < int(c.b); i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := c.kid(c.a + i).writeJSON(b); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case itMap:
		order := make([]int, int(c.b))
		for i := range order {
			order[i] = i
		}
		sort.Slice(order, func(x, y int) bool {
			return string(c.kid(c.a+2*order[x]).bytes()) < string(c.kid(c.a+2*order[y]).bytes())
		})
		b.WriteByte('{')
		n := 0
		for _, i := range order {
			if c.kid(c.a + 2*i + 1).undefined() {
				continue
			}
			if n > 0 {
				b.WriteByte(',')
			}
			n++
			enc, err := json.Marshal(string(c.kid(c.a + 2*i).bytes()))
			if err != nil {
				return errNoJSONForm("a key that is not valid UTF-8")
			}
			b.Write(enc)
			b.WriteByte(':')
			if err := c.kid(c.a + 2*i + 1).writeJSON(b); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case itBytes, itUUID:
		return errNoJSONForm("a byte string")
	case itInstant:
		return errNoJSONForm("a date and time")
	case itDate:
		return errNoJSONForm("a date")
	case itOpaqueMap:
		return errNoJSONForm("a map with a key that is not text")
	case itSimple:
		return errNoJSONForm(fmt.Sprintf("simple value %d", c.extra))
	default:
		return errNoJSONForm(fmt.Sprintf("a value with CBOR tag %d", c.extra))
	}
	return nil
}
