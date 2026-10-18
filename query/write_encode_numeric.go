package query

import (
	"strconv"
	"strings"

	"github.com/samber/oops"
)

// Postgres' numeric binary form : ndigits, weight, sign, dscale (int16 each), then
// ndigits base-10000 digits, the first one holding the weight'th power of 10000.
const (
	numericPos    = 0x0000
	numericNeg    = 0x4000
	numericNaN    = 0xC000
	numericPosInf = 0xD000
	numericNegInf = 0xF000

	numericMaxDscale = 16383 // NUMERIC_DSCALE_MAX
)

// numericSpecial maps the words numeric input reads besides numbers, lower-cased.
func numericSpecial(s string) (sign uint16, ok bool) {
	switch strings.ToLower(s) {
	case "nan":
		return numericNaN, true
	case "infinity", "+infinity", "inf", "+inf":
		return numericPosInf, true
	case "-infinity", "-inf":
		return numericNegInf, true
	}
	return 0, false
}

func encodeNumeric(dst []byte, n Node, _ *encodeContext) ([]byte, error) {
	var text string
	var err error
	switch n.Kind() {
	case KindNumber:
		text, err = numberText(n)
	case KindString:
		text, err = n.StrictString()
	default:
		return nil, errWrongKind("numeric", n)
	}
	if err != nil {
		return nil, oops.Wrapf(err, "write: reading numeric")
	}
	return appendNumeric(dst, text)
}

// appendNumeric parses text (optional sign, digits with an optional point, optional
// exponent ; or NaN / Infinity words) into numeric's binary form, from the
// exact digits : no float is involved and the display scale is kept ("1.50" stays 1.50).
func appendNumeric(dst []byte, text string) ([]byte, error) {
	if sign, ok := numericSpecial(text); ok {
		dst = appendInt16(dst, 0)
		dst = appendInt16(dst, 0)
		dst = binaryAppendUint16(dst, sign)
		return appendInt16(dst, 0), nil
	}

	s := text
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	intPart := s[:i]
	fracPart := ""
	if i < len(s) && s[i] == '.' {
		j := i + 1
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		fracPart = s[i+1 : j]
		i = j
	}
	if intPart == "" && fracPart == "" {
		return nil, errBadValue("numeric", text, "no digits")
	}
	exp := 0
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		e := s[i+1:]
		if e == "" || len(e) > 9+1 {
			return nil, errBadValue("numeric", text, "bad exponent")
		}
		v, err := strconv.Atoi(e)
		if err != nil || strings.ContainsAny(e[1:], "+-") {
			return nil, errBadValue("numeric", text, "bad exponent")
		}
		exp = v
		i = len(s)
	}
	if i != len(s) {
		return nil, errBadValue("numeric", text, "unexpected character")
	}

	digits := intPart + fracPart
	pointPos := len(intPart) + exp // where the decimal point sits, counted in digits from the start
	dscale := len(fracPart) - exp
	if dscale < 0 {
		dscale = 0
	}
	if dscale > numericMaxDscale {
		return nil, errOutOfRange("numeric", text)
	}

	lead := 0
	for lead < len(digits) && digits[lead] == '0' {
		lead++
	}
	digits = digits[lead:]
	pointPos -= lead
	digits = strings.TrimRight(digits, "0")
	if digits == "" { // zero, which keeps its display scale
		dst = appendInt16(dst, 0)
		dst = appendInt16(dst, 0)
		dst = binaryAppendUint16(dst, numericPos)
		return appendInt16(dst, int16(dscale)), nil
	}

	// Align the digits on groups of four around the decimal point.
	lpad := ((-pointPos)%4 + 4) % 4
	weight := (pointPos+lpad)/4 - 1
	total := lpad + len(digits)
	ngroups := (total + 3) / 4
	if ngroups > 0x7fff || weight > 0x7fff || weight < -0x8000 {
		return nil, errOutOfRange("numeric", text)
	}

	sign := uint16(numericPos)
	if neg {
		sign = numericNeg
	}
	dst = appendInt16(dst, int16(ngroups))
	dst = appendInt16(dst, int16(weight))
	dst = binaryAppendUint16(dst, sign)
	dst = appendInt16(dst, int16(dscale))

	// Digit k of the padded string is lpad zeros, then digits, then zeros to fill the last group.
	at := func(k int) int {
		k -= lpad
		if k < 0 || k >= len(digits) {
			return 0
		}
		return int(digits[k] - '0')
	}
	for g := 0; g < ngroups; g++ {
		k := g * 4
		dst = appendInt16(dst, int16(at(k)*1000+at(k+1)*100+at(k+2)*10+at(k+3)))
	}
	return dst, nil
}
