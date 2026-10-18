package cborenc

import (
	"fmt"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// AppendJSON converts one JSON document to CBOR and appends it to dst, reading the document straight from
// its text, with no tree built :
//
//   - an object or an array becomes an indefinite-length map or array ;
//   - a number without '.', 'e' or 'E' is an integer, or a bignum (tag 2 or 3) beyond 64 bits ;
//   - any other number is a decimal fraction (tag 4, [exponent, mantissa]) read from its digits, never through a
//     float, so its trailing zeros stay : 1.50 is [-2, 150] ;
//   - a string is a text string, with its escapes resolved ; bytes that are not UTF-8 read as U+FFFD.
//
// On error dst is returned as it was.
func AppendJSON(dst, src []byte) ([]byte, error) {
	p := jsonParser{src: src}
	out, err := p.value(dst, 0)
	if err != nil {
		return dst, err
	}
	p.skipSpace()
	if p.pos != len(src) {
		return dst, p.fail("data after the document")
	}
	return out, nil
}

const maxJSONDepth = 1000

type jsonParser struct {
	src []byte
	pos int
}

func (p *jsonParser) fail(format string, args ...any) error {
	return fmt.Errorf("cborenc: invalid JSON at byte %d: %s", p.pos, fmt.Sprintf(format, args...))
}

func (p *jsonParser) skipSpace() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *jsonParser) value(dst []byte, depth int) ([]byte, error) {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return dst, p.fail("unexpected end of data")
	}
	switch c := p.src[p.pos]; {
	case c == '{':
		return p.object(dst, depth)
	case c == '[':
		return p.array(dst, depth)
	case c == '"':
		s, err := p.str()
		if err != nil {
			return dst, err
		}
		return AppendTextBytes(dst, s), nil
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number(dst)
	case c == 't':
		return p.literal(dst, "true", AppendBool(nil, true))
	case c == 'f':
		return p.literal(dst, "false", AppendBool(nil, false))
	case c == 'n':
		return p.literal(dst, "null", AppendNull(nil))
	default:
		return dst, p.fail("unexpected byte %q", c)
	}
}

func (p *jsonParser) literal(dst []byte, word string, cbor []byte) ([]byte, error) {
	if len(p.src)-p.pos < len(word) || string(p.src[p.pos:p.pos+len(word)]) != word {
		return dst, p.fail("expected %s", word)
	}
	p.pos += len(word)
	return append(dst, cbor...), nil
}

func (p *jsonParser) array(dst []byte, depth int) ([]byte, error) {
	if depth >= maxJSONDepth {
		return dst, p.fail("nested more than %d levels", maxJSONDepth)
	}
	p.pos++
	dst = append(dst, IndefiniteArray)
	p.skipSpace()
	if p.pos < len(p.src) && p.src[p.pos] == ']' {
		p.pos++
		return append(dst, Break), nil
	}
	for {
		var err error
		if dst, err = p.value(dst, depth+1); err != nil {
			return dst, err
		}
		p.skipSpace()
		if p.pos >= len(p.src) {
			return dst, p.fail("unexpected end of data in an array")
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return append(dst, Break), nil
		default:
			return dst, p.fail("expected ',' or ']'")
		}
	}
}

func (p *jsonParser) object(dst []byte, depth int) ([]byte, error) {
	if depth >= maxJSONDepth {
		return dst, p.fail("nested more than %d levels", maxJSONDepth)
	}
	p.pos++
	dst = append(dst, IndefiniteMap)
	p.skipSpace()
	if p.pos < len(p.src) && p.src[p.pos] == '}' {
		p.pos++
		return append(dst, Break), nil
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] != '"' {
			return dst, p.fail("expected a string key")
		}
		key, err := p.str()
		if err != nil {
			return dst, err
		}
		dst = AppendTextBytes(dst, key)
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			return dst, p.fail("expected ':'")
		}
		p.pos++
		if dst, err = p.value(dst, depth+1); err != nil {
			return dst, err
		}
		p.skipSpace()
		if p.pos >= len(p.src) {
			return dst, p.fail("unexpected end of data in an object")
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return append(dst, Break), nil
		default:
			return dst, p.fail("expected ',' or '}'")
		}
	}
}

// str reads the string at p.pos (a '"') and returns its resolved bytes.
func (p *jsonParser) str() ([]byte, error) {
	p.pos++
	start := p.pos
	// the common case : no escape, so the bytes are the string
	for p.pos < len(p.src) && p.src[p.pos] != '"' && p.src[p.pos] != '\\' && p.src[p.pos] >= 0x20 {
		p.pos++
	}
	if p.pos < len(p.src) && p.src[p.pos] == '"' {
		s := p.src[start:p.pos]
		p.pos++
		return validUTF8(s), nil
	}
	out := append([]byte(nil), p.src[start:p.pos]...)
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '"':
			p.pos++
			return validUTF8(out), nil
		case c < 0x20:
			return nil, p.fail("a control character in a string")
		case c != '\\':
			out = append(out, c)
			p.pos++
			continue
		}
		p.pos++
		if p.pos >= len(p.src) {
			break
		}
		e := p.src[p.pos]
		p.pos++
		switch e {
		case '"', '\\', '/':
			out = append(out, e)
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			r, ok := p.hex4()
			if !ok {
				return nil, p.fail("a bad \\u escape")
			}
			if utf16.IsSurrogate(r) {
				// a pair is two escapes ; a lone half is replaced
				if r < 0xdc00 && p.pos+1 < len(p.src) && p.src[p.pos] == '\\' && p.src[p.pos+1] == 'u' {
					save := p.pos
					p.pos += 2
					if r2, ok := p.hex4(); ok && r2 >= 0xdc00 && r2 < 0xe000 {
						r = utf16.DecodeRune(r, r2)
					} else {
						p.pos = save
						r = utf8.RuneError
					}
				} else {
					r = utf8.RuneError
				}
			}
			out = utf8.AppendRune(out, r)
		default:
			return nil, p.fail("a bad escape \\%c", e)
		}
	}
	return nil, p.fail("unexpected end of data in a string")
}

func (p *jsonParser) hex4() (rune, bool) {
	if p.pos+4 > len(p.src) {
		return 0, false
	}
	var r rune
	for _, c := range p.src[p.pos : p.pos+4] {
		var v byte
		switch {
		case c >= '0' && c <= '9':
			v = c - '0'
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v = c - 'A' + 10
		default:
			return 0, false
		}
		r = r<<4 | rune(v)
	}
	p.pos += 4
	return r, true
}

func validUTF8(b []byte) []byte {
	if utf8.Valid(b) {
		return b
	}
	out := make([]byte, 0, len(b))
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		out = utf8.AppendRune(out, r)
		b = b[n:]
	}
	return out
}

// number reads a JSON number and appends it as an integer or a decimal fraction.
func (p *jsonParser) number(dst []byte) ([]byte, error) {
	start := p.pos
	if p.src[p.pos] == '-' {
		p.pos++
	}
	digits := func() int {
		n := 0
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
			n++
		}
		return n
	}
	intStart := p.pos
	if p.pos < len(p.src) && p.src[p.pos] == '0' {
		p.pos++
	} else if digits() == 0 {
		return dst, p.fail("a number needs digits")
	}
	intEnd := p.pos
	fracStart, fracEnd := p.pos, p.pos
	if p.pos < len(p.src) && p.src[p.pos] == '.' {
		p.pos++
		fracStart = p.pos
		if digits() == 0 {
			return dst, p.fail("a fraction needs digits")
		}
		fracEnd = p.pos
	}
	var exp int64
	hasExp := false
	if p.pos < len(p.src) && (p.src[p.pos] == 'e' || p.src[p.pos] == 'E') {
		hasExp = true
		p.pos++
		expStart := p.pos
		if p.pos < len(p.src) && (p.src[p.pos] == '+' || p.src[p.pos] == '-') {
			p.pos++
		}
		if digits() == 0 {
			return dst, p.fail("an exponent needs digits")
		}
		var err error
		if exp, err = strconv.ParseInt(string(p.src[expStart:p.pos]), 10, 64); err != nil {
			return dst, p.fail("an exponent out of range")
		}
	}
	text := string(p.src[start:p.pos])
	if fracEnd == fracStart && !hasExp {
		return AppendInteger(dst, text), nil
	}
	mantissa := string(p.src[intStart:intEnd]) + string(p.src[fracStart:fracEnd])
	if p.src[start] == '-' {
		mantissa = "-" + mantissa
	}
	return AppendDecimal(dst, mantissa, exp-int64(fracEnd-fracStart)), nil
}
