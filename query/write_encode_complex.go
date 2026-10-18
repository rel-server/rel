package query

import (
	"strings"

	"github.com/rel-server/rel/pg"
	"github.com/samber/oops"
)

// columnEncoder returns the encoder for a value of type t and whether it is
// in binary mode. Binary mode needs an encoder for every type in t's tree ; any
// other type is in text mode, whose encoder writes the text Postgres' input
// function reads (specs/typed-write-loading.md ## Column mode).
func columnEncoder(t *pg.Type) (enc fieldEncoder, binary bool) {
	if enc := binaryEncoderFor(t); enc != nil {
		return enc, true
	}
	return textModeEncoder(t), false
}

// binaryEncoderFor returns the binary encoder of t, or nil when some type in its tree has none.
func binaryEncoderFor(t *pg.Type) fieldEncoder {
	if enc := scalarEncoderFor(t); enc != nil {
		return enc
	}
	u := t.Underlying()
	switch {
	case u.IsArray():
		elem := binaryEncoderFor(u.ElementType)
		if elem == nil {
			return nil
		}
		return arrayEncoder(u.ElementType, elem)
	case u.IsComposite():
		c := compositeEncoderFor(u)
		if c == nil {
			return nil
		}
		return c.encode
	}
	return nil
}

// ---- arrays -------------------------------------------------------------------------

// arrayShape returns the sizes of the dimensions n holds, checking the nesting is
// rectangular. A size of zero anywhere makes an empty array, which has no dimension at all.
func arrayShape(n Node) (sizes []int, leaves []Node, err error) {
	if n.Kind() != KindArray {
		return nil, nil, errWrongKind("array", n)
	}
	items, err := n.ArrayItems()
	if err != nil {
		return nil, nil, oops.Wrapf(err, "write: reading array")
	}
	sizes = []int{len(items)}
	if len(items) == 0 {
		return nil, nil, nil
	}
	if items[0].Kind() != KindArray { // the first element decides whether there is another dimension
		return sizes, items, nil
	}
	var inner []int
	for i, it := range items {
		s, l, err := arrayShape(it)
		if err != nil {
			return nil, nil, err
		}
		if s == nil && len(l) == 0 && i == 0 {
			// the first sub-array is empty : the whole array is empty
			continue
		}
		if i == 0 {
			inner = s
		} else if !sameInts(inner, s) {
			return nil, nil, oops.With("received", kindName(KindArray)).Errorf("write: the arrays are not rectangular: sub-array %d has a different shape from the first", i)
		}
		leaves = append(leaves, l...)
	}
	if len(leaves) == 0 {
		return nil, nil, nil
	}
	return append(sizes, inner...), leaves, nil
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func arrayEncoder(elemType *pg.Type, elem fieldEncoder) fieldEncoder {
	elemOID := uint32(elemType.PgOid)
	return func(dst []byte, n Node, ec *encodeContext) ([]byte, error) {
		sizes, leaves, err := arrayShape(n)
		if err != nil {
			return nil, err
		}
		hasNull := false
		for _, l := range leaves {
			if l.Kind() == KindNull {
				hasNull = true
				break
			}
		}
		dst = appendInt32(dst, int32(len(sizes)))
		if hasNull {
			dst = appendInt32(dst, 1)
		} else {
			dst = appendInt32(dst, 0)
		}
		dst = binaryAppendUint32(dst, elemOID)
		for _, s := range sizes {
			dst = appendInt32(dst, int32(s))
			dst = appendInt32(dst, 1) // lower bound
		}
		for i, l := range leaves {
			if l.Kind() == KindNull {
				dst = appendInt32(dst, -1)
				continue
			}
			var err error
			if dst, err = appendLengthPrefixed(dst, l, ec, elem); err != nil {
				return nil, oops.With("element", i).Wrapf(err, "write: array element %d", i)
			}
		}
		return dst, nil
	}
}

// appendLengthPrefixed appends a 4-byte length, then enc's bytes for n.
func appendLengthPrefixed(dst []byte, n Node, ec *encodeContext, enc fieldEncoder) ([]byte, error) {
	at := len(dst)
	dst = append(dst, 0, 0, 0, 0)
	dst, err := enc(dst, n, ec)
	if err != nil {
		return nil, err
	}
	putInt32(dst[at:], int32(len(dst)-at-4))
	return dst, nil
}

func putInt32(b []byte, v int32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

func binaryAppendUint32(dst []byte, v uint32) []byte { return appendInt32(dst, int32(v)) }

// ---- composites ---------------------------------------------------------------------

type compositeField struct {
	name string
	oid  uint32
	enc  fieldEncoder
}

type compositeEncoder struct {
	fields []compositeField
}

// compositeEncoderFor returns the binary encoder of composite type u, or nil when a
// field's type has none.
func compositeEncoderFor(u *pg.Type) *compositeEncoder {
	c := &compositeEncoder{}
	for _, col := range u.Relation.Columns {
		enc := binaryEncoderFor(col.Type)
		if enc == nil {
			return nil
		}
		c.fields = append(c.fields, compositeField{name: col.Name, oid: uint32(col.Type.PgOid), enc: enc})
	}
	return c
}

func (c *compositeEncoder) encode(dst []byte, n Node, ec *encodeContext) ([]byte, error) {
	return c.encodeMarking(dst, n, ec, nil)
}

// encodeMarking appends the binary record for object n ; mark, when set, is told
// which fields n supplied. A key n lacks is NULL, a key that is not a field is ignored.
func (c *compositeEncoder) encodeMarking(dst []byte, n Node, ec *encodeContext, mark func(field int)) ([]byte, error) {
	if n.Kind() != KindObject {
		return nil, errWrongKind("composite", n)
	}
	dst = appendInt32(dst, int32(len(c.fields)))
	for i, f := range c.fields {
		dst = binaryAppendUint32(dst, f.oid)
		v, ok := n.ObjectGet(f.name)
		if !ok {
			dst = appendInt32(dst, -1)
			continue
		}
		if mark != nil {
			mark(i)
		}
		if v.Kind() == KindNull {
			dst = appendInt32(dst, -1)
			continue
		}
		var err error
		if dst, err = appendLengthPrefixed(dst, v, ec, f.enc); err != nil {
			return nil, oops.With("field", f.name).Wrapf(err, "write: field %q", f.name)
		}
	}
	return dst, nil
}

// ---- text mode ----------------------------------------------------------------------

// textModeEncoder returns the encoder writing t's value as the text its input
// function reads : a scalar as the text of its JSON value, an array or composite as a literal.
func textModeEncoder(t *pg.Type) fieldEncoder {
	u := t.Underlying()
	switch {
	case u.IsArray():
		return textArrayEncoder(u.ElementType)
	case u.IsComposite():
		return textCompositeEncoder(u)
	}
	return encodeScalarText
}

// encodeScalarText writes a scalar's value in text form : a string as is, a number as its
// digits, a boolean as true or false, an object or array as its JSON text.
func encodeScalarText(dst []byte, n Node, _ *encodeContext) ([]byte, error) {
	switch n.Kind() {
	case KindString:
		s, err := n.StrictString()
		if err != nil {
			return nil, oops.Wrapf(err, "write: reading text")
		}
		if strings.IndexByte(s, 0) >= 0 {
			return nil, errBadCharacter("text", s)
		}
		return append(dst, s...), nil
	case KindNumber, KindObject, KindArray:
		raw, err := n.Raw()
		if err != nil {
			return nil, oops.Wrapf(err, "write: reading value")
		}
		return append(dst, raw...), nil
	case KindBool:
		b, err := n.StrictBool()
		if err != nil {
			return nil, oops.Wrapf(err, "write: reading boolean")
		}
		if b {
			return append(dst, "true"...), nil
		}
		return append(dst, "false"...), nil
	}
	return nil, errWrongKind("text", n)
}

// appendQuoted appends text as an element of an array literal (inArray) or a field
// of a composite literal, quoted when it needs to be.
func appendQuoted(dst []byte, text []byte, inArray bool) []byte {
	need := len(text) == 0
	if inArray && len(text) == 4 && strings.EqualFold(string(text), "null") {
		need = true
	}
	for _, c := range text {
		switch c {
		case '"', '\\', ',', ' ', '\t', '\n', '\r', '\f', '\v':
			need = true
		case '{', '}':
			need = need || inArray
		case '(', ')':
			need = need || !inArray
		}
	}
	if !need {
		return append(dst, text...)
	}
	dst = append(dst, '"')
	for _, c := range text {
		if c == '"' || c == '\\' {
			dst = append(dst, '\\')
		}
		dst = append(dst, c)
	}
	return append(dst, '"')
}

func textArrayEncoder(elemType *pg.Type) fieldEncoder {
	elem := textModeEncoder(elemType)
	return func(dst []byte, n Node, ec *encodeContext) ([]byte, error) {
		return appendTextArray(dst, n, ec, elem)
	}
}

func appendTextArray(dst []byte, n Node, ec *encodeContext, elem fieldEncoder) ([]byte, error) {
	if n.Kind() != KindArray {
		return nil, errWrongKind("array", n)
	}
	items, err := n.ArrayItems()
	if err != nil {
		return nil, oops.Wrapf(err, "write: reading array")
	}
	dst = append(dst, '{')
	var scratch []byte
	nested := len(items) > 0 && items[0].Kind() == KindArray // the first element decides whether there is another dimension
	for i, it := range items {
		if i > 0 {
			dst = append(dst, ',')
		}
		switch {
		case it.Kind() == KindNull:
			dst = append(dst, "NULL"...)
		case nested:
			if dst, err = appendTextArray(dst, it, ec, elem); err != nil {
				return nil, err
			}
		default:
			if scratch, err = elem(scratch[:0], it, ec); err != nil {
				return nil, oops.With("element", i).Wrapf(err, "write: array element %d", i)
			}
			dst = appendQuoted(dst, scratch, true)
		}
	}
	return append(dst, '}'), nil
}

func textCompositeEncoder(u *pg.Type) fieldEncoder {
	type field struct {
		name string
		enc  fieldEncoder
	}
	var fields []field
	for _, col := range u.Relation.Columns {
		fields = append(fields, field{col.Name, textModeEncoder(col.Type)})
	}
	return func(dst []byte, n Node, ec *encodeContext) ([]byte, error) {
		if n.Kind() != KindObject {
			return nil, errWrongKind("composite", n)
		}
		dst = append(dst, '(')
		var scratch []byte
		for i, f := range fields {
			if i > 0 {
				dst = append(dst, ',')
			}
			v, ok := n.ObjectGet(f.name)
			if !ok || v.Kind() == KindNull {
				continue // a NULL field is empty and unquoted
			}
			var err error
			if scratch, err = f.enc(scratch[:0], v, ec); err != nil {
				return nil, oops.With("field", f.name).Wrapf(err, "write: field %q", f.name)
			}
			dst = appendQuoted(dst, scratch, false)
		}
		return append(dst, ')'), nil
	}
}
