package query

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/samber/oops"
)

// The CBOR adapter (specs/cbor.md ## CBOR adapter) : a Node over a CBOR document, read in one pass over
// its bytes into a flat arena. Nothing is decoded into Go values : a node records what kind of item it is and where
// its data sits in the request body, and a string or a number is only built when a column asks for it. The pass
// also validates the whole document, so a malformed body is refused before anything else happens.
//
// The reference implementation this one is tested against, over fxamacker/cbor's decode into any, is in node_cbor_ref_test.go.

const (
	cborMaxDepth = 64 // nesting of arrays, maps and tags
)

// NewCBORNode reads one CBOR data item, which must be all of data. The node refers to data, which the
// caller must not change while the node is in use.
func NewCBORNode(data []byte) (Node, error) {
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
	itBadTag  // a tag this adapter has no meaning for, or one with the wrong content : extra holds its number
)

// cnode is one item of the document.
type cnode struct {
	doc     *cborDoc
	item    cborItem
	chunked bool // an indefinite-length string or byte string, assembled in blobs
	a, b    int
	extra   uint64
}

type cborDoc struct {
	data  []byte
	nodes []cnode
	kids  []int32 // the elements of arrays and the keys and values of maps, by index in nodes
	strs  []string
	blobs [][]byte
	times []time.Time
}

// ---- reading ------------------------------------------------------------------------

type cborDecoder struct {
	doc *cborDoc
	pos int
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
		return 0, 0, 0, false, fmt.Errorf("unexpected end of data")
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
			return 0, 0, 0, false, fmt.Errorf("unexpected end of data")
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
	return 0, 0, 0, false, fmt.Errorf("reserved additional information %d", ai)
}

// item reads one data item and returns its index in the arena.
func (d *cborDecoder) item(depth int) (int32, error) {
	major, ai, val, indef, err := d.head()
	if err != nil {
		return 0, err
	}
	switch major {
	case 0:
		if indef {
			return 0, fmt.Errorf("an integer cannot have an indefinite length")
		}
		if val > math.MaxInt64 {
			return d.add(cnode{item: itUint, extra: val}), nil
		}
		return d.add(cnode{item: itInt, extra: val}), nil
	case 1:
		if indef {
			return 0, fmt.Errorf("an integer cannot have an indefinite length")
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
			return 0, fmt.Errorf("a tag cannot have an indefinite length")
		}
		return d.tagItem(val, depth)
	}
	return d.simpleItem(ai, val, indef)
}

func (d *cborDecoder) simpleItem(ai byte, val uint64, indef bool) (int32, error) {
	switch {
	case indef:
		return 0, fmt.Errorf("unexpected break")
	case ai == 20:
		return d.add(cnode{item: itFalse}), nil
	case ai == 21:
		return d.add(cnode{item: itTrue}), nil
	case ai == 22 || ai == 23: // null, and undefined which reads as null
		return d.add(cnode{item: itNull}), nil
	case ai == 25:
		return d.add(cnode{item: itFloat, extra: math.Float64bits(float16ToFloat64(uint16(val)))}), nil
	case ai == 26:
		return d.add(cnode{item: itFloat, extra: math.Float64bits(float64(math.Float32frombits(uint32(val))))}), nil
	case ai == 27:
		return d.add(cnode{item: itFloat, extra: val}), nil
	}
	return 0, fmt.Errorf("unsupported simple value %d", val)
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
			return 0, fmt.Errorf("a string of %d bytes runs past the end of the data", length)
		}
		off := d.pos
		d.pos += int(length)
		if kind == itText && !utf8.Valid(d.doc.data[off:d.pos]) {
			return 0, fmt.Errorf("a text string that is not valid UTF-8")
		}
		return d.add(cnode{item: kind, a: off, b: int(length)}), nil
	}
	// chunks of the same major type, each a definite-length string, up to a break
	var assembled []byte
	for {
		if d.remaining() < 1 {
			return 0, fmt.Errorf("unexpected end of data in an indefinite-length string")
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
			return 0, fmt.Errorf("a chunk of an indefinite-length string that is not a definite-length string of the same type")
		}
		if n > uint64(d.remaining()) {
			return 0, fmt.Errorf("a chunk of %d bytes runs past the end of the data", n)
		}
		chunk := d.doc.data[d.pos : d.pos+int(n)]
		d.pos += int(n)
		if kind == itText && !utf8.Valid(chunk) {
			return 0, fmt.Errorf("a text chunk that is not valid UTF-8")
		}
		assembled = append(assembled, chunk...)
	}
	d.doc.blobs = append(d.doc.blobs, assembled)
	return d.add(cnode{item: kind, chunked: true, a: len(d.doc.blobs) - 1, b: len(assembled)}), nil
}

// countFits checks that a container header's count can be backed by the bytes left : every element takes at least one.
func (d *cborDecoder) countFits(n uint64, perElement int) error {
	if n > uint64(d.remaining())/uint64(perElement) {
		return fmt.Errorf("a count of %d runs past the end of the data", n)
	}
	return nil
}

func (d *cborDecoder) arrayItem(n uint64, indef bool, depth int) (int32, error) {
	if depth >= cborMaxDepth {
		return 0, fmt.Errorf("nested more than %d levels", cborMaxDepth)
	}
	if indef {
		var elems []int32
		for {
			if d.remaining() < 1 {
				return 0, fmt.Errorf("unexpected end of data in an indefinite-length array")
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
		return d.add(cnode{item: itArray, a: start, b: len(elems)}), nil
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
	return d.add(cnode{item: itArray, a: start, b: int(n)}), nil
}

func (d *cborDecoder) mapItem(n uint64, indef bool, depth int) (int32, error) {
	if depth >= cborMaxDepth {
		return 0, fmt.Errorf("nested more than %d levels", cborMaxDepth)
	}
	var kids []int32 // used for an indefinite map, whose size is not known up front
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
				return 0, fmt.Errorf("unexpected end of data in an indefinite-length map")
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
			return 0, fmt.Errorf("a map key that is not a text string")
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
	return d.add(cnode{item: itMap, a: start, b: count}), nil
}

func (d *cborDecoder) checkDistinctKeys(start, pairs int) error {
	key := func(i int) []byte { return d.doc.nodes[d.doc.kids[start+2*i]].textBytes() }
	if pairs <= 8 {
		for i := 1; i < pairs; i++ {
			for j := 0; j < i; j++ {
				if string(key(i)) == string(key(j)) {
					return fmt.Errorf("duplicate map key %q", key(i))
				}
			}
		}
		return nil
	}
	seen := make(map[string]struct{}, pairs)
	for i := 0; i < pairs; i++ {
		k := string(key(i))
		if _, dup := seen[k]; dup {
			return fmt.Errorf("duplicate map key %q", k)
		}
		seen[k] = struct{}{}
	}
	return nil
}

// tagItem reads a tag and its content, and gives the pair the meaning this adapter knows for it.
func (d *cborDecoder) tagItem(tag uint64, depth int) (int32, error) {
	if depth >= cborMaxDepth {
		return 0, fmt.Errorf("nested more than %d levels", cborMaxDepth)
	}
	c, err := d.item(depth + 1)
	if err != nil {
		return 0, err
	}
	content := d.doc.nodes[c]
	bad := func() (int32, error) { return d.add(cnode{item: itBadTag, extra: tag}), nil }

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
			return 0, fmt.Errorf("a tag 0 value that is not an RFC 3339 date and time: %w", err)
		}
		return d.addInstant(t), nil
	case 1:
		var t time.Time
		switch content.item {
		case itInt:
			t = time.Unix(int64(content.extra), 0)
		case itUint:
			return 0, fmt.Errorf("a tag 1 value out of range")
		case itFloat:
			f := math.Float64frombits(content.extra)
			if math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > 9e18 { // beyond what a time.Time can hold
				return 0, fmt.Errorf("a tag 1 value out of range")
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

func (d *cborDecoder) addText(item cborItem, text string, b int, extra uint64) int32 {
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
	return c.doc.data[c.a : c.a+c.b]
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
	return fmt.Sprintf("CBOR tag %d with this content", c.extra)
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

func (c *cnode) ArrayItems() ([]Node, error) {
	if c.item != itArray {
		return nil, errNotArray
	}
	out := make([]Node, c.b)
	for i := range out {
		out[i] = c.kid(c.a + i)
	}
	return out, nil
}

func (c *cnode) ObjectGet(key string) (Node, bool) {
	if c.item != itMap {
		return nil, false
	}
	for i := 0; i < c.b; i++ {
		if string(c.kid(c.a+2*i).bytes()) == key {
			return c.kid(c.a + 2*i + 1), true
		}
	}
	return nil, false
}

func (c *cnode) ObjectEach(fn func(string, Node) error) error {
	if c.item != itMap {
		return errNotObject
	}
	for i := 0; i < c.b; i++ {
		if err := fn(string(c.kid(c.a+2*i).bytes()), c.kid(c.a+2*i+1)); err != nil {
			return err
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
		for i := 0; i < c.b; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := c.kid(c.a + i).writeJSON(b); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case itMap:
		order := make([]int, c.b)
		for i := range order {
			order[i] = i
		}
		sort.Slice(order, func(x, y int) bool {
			return string(c.kid(c.a+2*order[x]).bytes()) < string(c.kid(c.a+2*order[y]).bytes())
		})
		b.WriteByte('{')
		for n, i := range order {
			if n > 0 {
				b.WriteByte(',')
			}
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
	default:
		return errNoJSONForm(fmt.Sprintf("a value with CBOR tag %d", c.extra))
	}
	return nil
}
