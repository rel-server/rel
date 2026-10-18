// Binary field encoders for the typed write path (specs/typed-write-loading.md
// ## Column encoders) : each turns a request Node into the bytes Postgres'
// binary receive function for the column's type reads. A column whose type has
// no encoder is loaded as text instead (text mode).
package query

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rel-server/rel/pg"
	"github.com/samber/oops"
)

// encodeContext carries what an encoder needs beyond the value : the
// connection's session time zone, used to read a timestamptz written without an offset.
type encodeContext struct {
	// Location is the session TimeZone ; nil means UTC.
	Location *time.Location

	// zoneName and zoneErr record a session TimeZone Go could not load : a timestamptz written
	// without an offset then cannot be placed, and says so.
	zoneName string
	zoneErr  error
}

func (ec *encodeContext) location() *time.Location {
	if ec == nil || ec.Location == nil {
		return time.UTC
	}
	return ec.Location
}

// fieldEncoder appends the binary form of n's value (without its length prefix) to
// dst. It is never called for a null : callers write a NULL field themselves.
type fieldEncoder func(dst []byte, n Node, ec *encodeContext) ([]byte, error)

// Built-in type OIDs, which are fixed across every Postgres database.
const (
	oidBool        = 16
	oidBytea       = 17
	oidName        = 19
	oidInt8        = 20
	oidInt2        = 21
	oidInt4        = 23
	oidText        = 25
	oidJSON        = 114
	oidFloat4      = 700
	oidFloat8      = 701
	oidBpchar      = 1042
	oidVarchar     = 1043
	oidDate        = 1082
	oidTimestamp   = 1114
	oidTimestamptz = 1184
	oidNumeric     = 1700
	oidUUID        = 2950
	oidJSONB       = 3802
)

var scalarEncoders = map[int]fieldEncoder{
	oidBool:        encodeBool,
	oidBytea:       encodeBytea,
	oidName:        encodeText,
	oidInt8:        encodeInt(64),
	oidInt2:        encodeInt(16),
	oidInt4:        encodeInt(32),
	oidText:        encodeText,
	oidJSON:        encodeJSON,
	oidFloat4:      encodeFloat(32),
	oidFloat8:      encodeFloat(64),
	oidBpchar:      encodeText,
	oidVarchar:     encodeText,
	oidDate:        encodeDate,
	oidTimestamp:   encodeTimestamp,
	oidTimestamptz: encodeTimestamptz,
	oidNumeric:     encodeNumeric,
	oidUUID:        encodeUUID,
	oidJSONB:       encodeJSONB,
}

// scalarEncoderFor returns the binary encoder of t, domains unwrapped, or nil
// when t has none. Arrays and composites are not scalars : see binaryEncoderFor.
func scalarEncoderFor(t *pg.Type) fieldEncoder {
	u := t.Underlying()
	if u == nil {
		return nil
	}
	if u.IsEnum() {
		return encodeText // an enum's binary form is its label
	}
	return scalarEncoders[u.PgOid]
}

// ---- errors -------------------------------------------------------------------------

func kindName(k NodeKind) string {
	switch k {
	case KindNull:
		return "null"
	case KindBool:
		return "boolean"
	case KindNumber:
		return "number"
	case KindString:
		return "string"
	case KindArray:
		return "array"
	case KindObject:
		return "object"
	case KindBytes:
		return "byte string"
	case KindInstant:
		return "date and time"
	case KindDate:
		return "date"
	case KindUUID:
		return "UUID"
	}
	return "invalid value"
}

// A value that cannot be written is reported as the error Postgres itself raises for the same
// value, SQLSTATE included, so it classifies and reaches the client the way it always has
// (server/rel.go classifyWriteError, pgerr.Classify).

// isDateTimeType reports whether typ is one of the date and time types.
func isDateTimeType(typ string) bool {
	return typ == "date" || typ == "timestamp" || typ == "timestamptz"
}

// errWrongKind reports a value of a kind the type does not accept : invalid_text_representation.
func errWrongKind(typ string, n Node) error {
	what := kindName(n.Kind())
	if u, ok := n.(interface{ unsupported() string }); ok && n.Kind() == KindInvalid {
		what = u.unsupported()
	}
	code, msg := "22P02", fmt.Sprintf("invalid input syntax for type %s: a %s value is not accepted", typ, what)
	if isDateTimeType(typ) {
		code = "22007"
	}
	return oops.With("type", typ, "received", what).Wrap(&pgconn.PgError{Severity: "ERROR", Code: code, Message: msg})
}

func shorten(text string) string {
	if len(text) > 64 {
		return text[:64] + "…"
	}
	return text
}

// errBadValue reports a value of the right kind that does not parse as typ : invalid_text_representation,
// or invalid_datetime_format for a date or time.
func errBadValue(typ, text, why string) error {
	text = shorten(text)
	code := "22P02"
	if isDateTimeType(typ) {
		code = "22007"
	}
	return oops.With("type", typ, "value", text).Wrap(&pgconn.PgError{
		Severity: "ERROR", Code: code,
		Message: fmt.Sprintf("invalid input syntax for type %s: %q", typ, text), Detail: why,
	})
}

// errOutOfRange reports a value that parses but does not fit the type : numeric_value_out_of_range, or
// datetime_field_overflow for a date or time.
func errOutOfRange(typ, text string) error {
	text = shorten(text)
	code, msg := "22003", fmt.Sprintf("value %q is out of range for type %s", text, typ)
	if isDateTimeType(typ) {
		code, msg = "22008", fmt.Sprintf("%s out of range: %q", typ, text)
	}
	return oops.With("type", typ, "value", text).Wrap(&pgconn.PgError{Severity: "ERROR", Code: code, Message: msg})
}

// errUnknownZone reports a timestamptz written without an offset while the session time zone is one Go
// cannot load : invalid_datetime_format, with a message that names the zone.
func errUnknownZone(text, zone string) error {
	text = shorten(text)
	return oops.With("type", "timestamptz", "value", text, "zone", zone).Wrap(&pgconn.PgError{
		Severity: "ERROR", Code: "22007",
		Message: fmt.Sprintf("cannot place %q in time zone %q, which rel does not know : write the time with an offset", text, zone),
	})
}

// errBadCharacter reports a string Postgres cannot store as text, a NUL : character_not_in_repertoire.
func errBadCharacter(typ, text string) error {
	return oops.With("type", typ, "value", shorten(text)).Wrap(&pgconn.PgError{
		Severity: "ERROR", Code: "22021", Message: "invalid byte sequence for encoding \"UTF8\": 0x00",
	})
}

// ---- helpers ------------------------------------------------------------------------

func appendInt16(dst []byte, v int16) []byte { return binary.BigEndian.AppendUint16(dst, uint16(v)) }
func appendInt32(dst []byte, v int32) []byte { return binary.BigEndian.AppendUint32(dst, uint32(v)) }
func appendInt64(dst []byte, v int64) []byte { return binary.BigEndian.AppendUint64(dst, uint64(v)) }

// numberText returns the source text of a number node.
func numberText(n Node) (string, error) {
	return n.Raw()
}

// ---- integers -----------------------------------------------------------------------

func encodeInt(bits int) fieldEncoder {
	name := "int" + strconv.Itoa(bits/8)
	return func(dst []byte, n Node, _ *encodeContext) ([]byte, error) {
		var text string
		var err error
		switch n.Kind() {
		case KindNumber:
			text, err = numberText(n)
		case KindString:
			text, err = n.StrictString()
		default:
			return nil, errWrongKind(name, n)
		}
		if err != nil {
			return nil, oops.Wrapf(err, "write: reading %s", name)
		}
		v, perr := strconv.ParseInt(text, 10, bits)
		if perr != nil {
			if ne, ok := perr.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
				return nil, errOutOfRange(name, text)
			}
			return nil, errBadValue(name, text, "not an integer")
		}
		switch bits {
		case 16:
			return appendInt16(dst, int16(v)), nil
		case 32:
			return appendInt32(dst, int32(v)), nil
		}
		return appendInt64(dst, v), nil
	}
}

// ---- floats -------------------------------------------------------------------------

// floatSpecial maps the words Postgres' float input reads, lower-cased.
func floatSpecial(s string) (float64, bool) {
	switch strings.ToLower(s) {
	case "nan":
		return math.NaN(), true
	case "infinity", "+infinity", "inf", "+inf":
		return math.Inf(1), true
	case "-infinity", "-inf":
		return math.Inf(-1), true
	}
	return 0, false
}

// isPlainDecimal reports whether s is made only of sign, digits, '.', 'e' and 'E' :
// strconv would also read hex floats and underscores, which Postgres does not.
func isPlainDecimal(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9') && c != '+' && c != '-' && c != '.' && c != 'e' && c != 'E' {
			return false
		}
	}
	return true
}

func encodeFloat(bits int) fieldEncoder {
	name := "float4"
	if bits == 64 {
		name = "float8"
	}
	return func(dst []byte, n Node, _ *encodeContext) ([]byte, error) {
		var text string
		var err error
		switch n.Kind() {
		case KindNumber:
			text, err = numberText(n)
		case KindString:
			text, err = n.StrictString()
		default:
			return nil, errWrongKind(name, n)
		}
		if err != nil {
			return nil, oops.Wrapf(err, "write: reading %s", name)
		}
		v, special := floatSpecial(text)
		if !special {
			if !isPlainDecimal(text) {
				return nil, errBadValue(name, text, "not a number")
			}
			var perr error
			if v, perr = strconv.ParseFloat(text, bits); perr != nil {
				return nil, errBadValue(name, text, "not a number or out of range")
			}
			if v == 0 && hasNonZeroDigit(text) {
				return nil, errBadValue(name, text, "underflow")
			}
		}
		if bits == 32 {
			return binary.BigEndian.AppendUint32(dst, math.Float32bits(float32(v))), nil
		}
		return binary.BigEndian.AppendUint64(dst, math.Float64bits(v)), nil
	}
}

// hasNonZeroDigit reports whether the mantissa of a plain decimal has a digit other than 0.
func hasNonZeroDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 'e' || c == 'E' {
			return false
		}
		if c >= '1' && c <= '9' {
			return true
		}
	}
	return false
}

// ---- bool ---------------------------------------------------------------------------

// parseBoolText reads the spellings Postgres' boolin accepts : any prefix of
// true, false, yes, no (case-insensitive), on, of, off, and 1 and 0, with surrounding
// whitespace allowed.
func parseBoolText(s string) (val, ok bool) {
	s = strings.Trim(s, " \t\n\r\f\v")
	if s == "" {
		return false, false
	}
	l := strings.ToLower(s)
	switch l[0] {
	case 't':
		return true, strings.HasPrefix("true", l)
	case 'f':
		return false, strings.HasPrefix("false", l)
	case 'y':
		return true, strings.HasPrefix("yes", l)
	case 'n':
		return false, strings.HasPrefix("no", l)
	case 'o':
		if l == "on" {
			return true, true
		}
		return false, len(l) >= 2 && strings.HasPrefix("off", l)
	case '1':
		return true, l == "1"
	case '0':
		return false, l == "0"
	}
	return false, false
}

func encodeBool(dst []byte, n Node, _ *encodeContext) ([]byte, error) {
	var v bool
	switch n.Kind() {
	case KindBool:
		b, err := n.StrictBool()
		if err != nil {
			return nil, oops.Wrapf(err, "write: reading bool")
		}
		v = b
	case KindNumber:
		text, err := numberText(n)
		if err != nil {
			return nil, oops.Wrapf(err, "write: reading bool")
		}
		if text != "0" && text != "1" {
			return nil, errBadValue("bool", text, "only 0 and 1 are accepted")
		}
		v = text == "1"
	case KindString:
		text, err := n.StrictString()
		if err != nil {
			return nil, oops.Wrapf(err, "write: reading bool")
		}
		b, ok := parseBoolText(text)
		if !ok {
			return nil, errBadValue("bool", text, "not a boolean spelling")
		}
		v = b
	default:
		return nil, errWrongKind("bool", n)
	}
	if v {
		return append(dst, 1), nil
	}
	return append(dst, 0), nil
}

// ---- text-like ----------------------------------------------------------------------

// encodeText serves text, varchar, bpchar, name and enums : the binary form is the string's bytes.
func encodeText(dst []byte, n Node, _ *encodeContext) ([]byte, error) {
	if n.Kind() != KindString {
		return nil, errWrongKind("text", n)
	}
	s, err := n.StrictString()
	if err != nil {
		return nil, oops.Wrapf(err, "write: reading text")
	}
	if strings.IndexByte(s, 0) >= 0 {
		return nil, errBadCharacter("text", s)
	}
	return append(dst, s...), nil
}

// ---- uuid ---------------------------------------------------------------------------

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// encodeUUID reads the hyphenated form, 32 bare hex digits, or either in braces.
func encodeUUID(dst []byte, n Node, _ *encodeContext) ([]byte, error) {
	if n.Kind() == KindUUID { // CBOR tag 37 : the 16 bytes
		b, err := n.(bytesNode).StrictBytes()
		if err != nil {
			return nil, oops.Wrapf(err, "write: reading uuid")
		}
		return append(dst, b...), nil
	}
	if n.Kind() != KindString {
		return nil, errWrongKind("uuid", n)
	}
	s, err := n.StrictString()
	if err != nil {
		return nil, oops.Wrapf(err, "write: reading uuid")
	}
	in := s
	if len(in) >= 2 && in[0] == '{' && in[len(in)-1] == '}' {
		in = in[1 : len(in)-1]
	}
	switch len(in) {
	case 36:
		if in[8] != '-' || in[13] != '-' || in[18] != '-' || in[23] != '-' {
			return nil, errBadValue("uuid", s, "hyphens in the wrong places")
		}
	case 32:
	default:
		return nil, errBadValue("uuid", s, "expected 32 hex digits, or the 36-character hyphenated form")
	}
	var out [16]byte
	k := 0
	hi := true
	var cur byte
	for i := 0; i < len(in); i++ {
		if in[i] == '-' && len(in) == 36 {
			continue
		}
		v, ok := hexVal(in[i])
		if !ok {
			return nil, errBadValue("uuid", s, "not a hex digit")
		}
		if hi {
			cur = v << 4
		} else {
			out[k] = cur | v
			k++
		}
		hi = !hi
	}
	return append(dst, out[:]...), nil
}

// ---- bytea --------------------------------------------------------------------------

// encodeBytea reads the hex input form, "\x" followed by pairs of hex digits.
func encodeBytea(dst []byte, n Node, _ *encodeContext) ([]byte, error) {
	if n.Kind() == KindBytes { // a CBOR byte string is the value itself
		b, err := n.(bytesNode).StrictBytes()
		if err != nil {
			return nil, oops.Wrapf(err, "write: reading bytea")
		}
		return append(dst, b...), nil
	}
	if n.Kind() != KindString {
		return nil, errWrongKind("bytea", n)
	}
	s, err := n.StrictString()
	if err != nil {
		return nil, oops.Wrapf(err, "write: reading bytea")
	}
	if !strings.HasPrefix(s, `\x`) {
		return nil, errBadValue("bytea", s, `expected the hex form, \x followed by hex digits`)
	}
	h := s[2:]
	if len(h)%2 != 0 {
		return nil, errBadValue("bytea", s, "odd number of hex digits")
	}
	for i := 0; i < len(h); i += 2 {
		a, ok1 := hexVal(h[i])
		b, ok2 := hexVal(h[i+1])
		if !ok1 || !ok2 {
			return nil, errBadValue("bytea", s, "not a hex digit")
		}
		dst = append(dst, a<<4|b)
	}
	return dst, nil
}

// ---- json / jsonb -------------------------------------------------------------------

func encodeJSON(dst []byte, n Node, _ *encodeContext) ([]byte, error) {
	raw, err := nodeJSON(n)
	if err != nil {
		return nil, oops.Wrapf(err, "write: reading json")
	}
	if strings.IndexByte(raw, 0) >= 0 {
		return nil, errBadCharacter("json", raw)
	}
	return append(dst, raw...), nil
}

func encodeJSONB(dst []byte, n Node, ec *encodeContext) ([]byte, error) {
	return encodeJSON(append(dst, 1), n, ec) // version byte, then the JSON text
}

func binaryAppendUint16(dst []byte, v uint16) []byte { return binary.BigEndian.AppendUint16(dst, v) }
