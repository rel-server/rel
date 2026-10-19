// Package cborenc writes CBOR (RFC 8949) by appending bytes : the data items a response
// needs, a streaming JSON to CBOR transcoder, and Accept header negotiation. Nothing here builds
// a Go value to encode, so a response can be written as it is produced.
package cborenc

import (
	"math"
	"math/big"
	"strings"
)

// Major types, already shifted into place.
const (
	majorUint   = 0 << 5
	majorNegInt = 1 << 5
	majorBytes  = 2 << 5
	majorText   = 3 << 5
	majorArray  = 4 << 5
	majorMap    = 5 << 5
	majorTag    = 6 << 5
	majorSimple = 7 << 5
)

// Bytes of the items that start or end a container of unknown length, and of the simple values.
const (
	IndefiniteArray = majorArray | 31
	IndefiniteMap   = majorMap | 31
	Break           = 0xff

	valueFalse = majorSimple | 20
	valueTrue  = majorSimple | 21
	valueNull  = majorSimple | 22
)

// Registered tags the encoders use.
const (
	TagDateTimeText   = 0
	TagEpochDateTime  = 1
	TagPositiveBignum = 2
	TagNegativeBignum = 3
	TagDecimal        = 4
	TagUUID           = 37
	TagEpochDate      = 100
)

// Structures (specs/cbor.md ## Structures) : the record extension of cbor-x. A definition is TagStructureDef over
// [StructureTagBase + id, [names], values...] and a reference is StructureTagBase + id over [values...].
const (
	TagStructureDef  = 0xdfff
	StructureTagBase = 0xe000
	// MaxStructures is how many ids a document can use : tags StructureTagBase to 0xfffe. Id 8191 is left out because
	// cbor-x looks a tag up as structure (tag & 0x1fff) before it tests for TagStructureDef, whose 0xdfff is 8191.
	MaxStructures = 0x1fff
)

// AppendStructureDef appends the head of a record's first use : the definition tag, the array header, the id and the
// names. The caller appends the len(names) values that follow.
func AppendStructureDef(dst []byte, id int, names []string) []byte {
	dst = AppendTag(dst, TagStructureDef)
	dst = AppendArrayHead(dst, len(names)+2)
	dst = AppendUint(dst, uint64(StructureTagBase+id))
	dst = AppendArrayHead(dst, len(names))
	for _, n := range names {
		dst = AppendText(dst, n)
	}
	return dst
}

// AppendStructureRef appends the head of a later use of structure id : the tag and the array header. The caller
// appends the n values that follow.
func AppendStructureRef(dst []byte, id, n int) []byte {
	dst = AppendTag(dst, uint64(StructureTagBase+id))
	return AppendArrayHead(dst, n)
}

// AppendHead appends the initial byte(s) of an item of the given major type (0 to 7, unshifted)
// whose argument is v, in the shortest form.
func AppendHead(dst []byte, major byte, v uint64) []byte {
	m := major << 5
	switch {
	case v < 24:
		return append(dst, m|byte(v))
	case v <= math.MaxUint8:
		return append(dst, m|24, byte(v))
	case v <= math.MaxUint16:
		return append(dst, m|25, byte(v>>8), byte(v))
	case v <= math.MaxUint32:
		return append(dst, m|26, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	}
	return append(dst, m|27, byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32), byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// AppendUint appends an unsigned integer.
func AppendUint(dst []byte, v uint64) []byte { return AppendHead(dst, 0, v) }

// AppendInt appends a signed integer.
func AppendInt(dst []byte, v int64) []byte {
	if v >= 0 {
		return AppendHead(dst, 0, uint64(v))
	}
	return AppendHead(dst, 1, uint64(-1-v))
}

// AppendFloat64 appends a double precision float.
func AppendFloat64(dst []byte, f float64) []byte {
	b := math.Float64bits(f)
	return append(dst, majorSimple|27, byte(b>>56), byte(b>>48), byte(b>>40), byte(b>>32), byte(b>>24), byte(b>>16), byte(b>>8), byte(b))
}

// AppendBool appends true or false.
func AppendBool(dst []byte, v bool) []byte {
	if v {
		return append(dst, valueTrue)
	}
	return append(dst, valueFalse)
}

// AppendNull appends null.
func AppendNull(dst []byte) []byte { return append(dst, valueNull) }

// AppendText appends a text string. s must be valid UTF-8.
func AppendText(dst []byte, s string) []byte {
	dst = AppendHead(dst, 3, uint64(len(s)))
	return append(dst, s...)
}

// AppendTextBytes is AppendText for a string held as bytes.
func AppendTextBytes(dst, s []byte) []byte {
	dst = AppendHead(dst, 3, uint64(len(s)))
	return append(dst, s...)
}

// AppendBytes appends a byte string.
func AppendBytes(dst, b []byte) []byte {
	dst = AppendHead(dst, 2, uint64(len(b)))
	return append(dst, b...)
}

// AppendTag appends a tag head ; the tagged item follows.
func AppendTag(dst []byte, tag uint64) []byte { return AppendHead(dst, 6, tag) }

// AppendArrayHead and AppendMapHead start a container of a known number of items (pairs, for a map).
func AppendArrayHead(dst []byte, n int) []byte { return AppendHead(dst, 4, uint64(n)) }
func AppendMapHead(dst []byte, n int) []byte   { return AppendHead(dst, 5, uint64(n)) }

// AppendInteger appends the integer written in digits (an optional '-', then decimal digits), as a
// CBOR integer when it fits 64 bits and as a bignum (tag 2 or 3) when it does not. The caller guarantees the text is
// well formed. "-0" is the integer 0.
func AppendInteger(dst []byte, digits string) []byte {
	neg := strings.HasPrefix(digits, "-")
	if neg {
		digits = digits[1:]
	}
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return AppendUint(dst, 0)
	}
	if len(digits) <= 19 { // fits a uint64 : 19 digits are below 1e19
		var v uint64
		for i := 0; i < len(digits); i++ {
			v = v*10 + uint64(digits[i]-'0')
		}
		if !neg {
			return AppendUint(dst, v)
		}
		return AppendHead(dst, 1, v-1)
	}
	n, _ := new(big.Int).SetString(digits, 10)
	if !neg && n.IsUint64() {
		return AppendUint(dst, n.Uint64())
	}
	if neg {
		n.Sub(n, big.NewInt(1)) // -1-n encodes n-1
		if n.IsUint64() {
			return AppendHead(dst, 1, n.Uint64())
		}
		return AppendBytes(AppendTag(dst, TagNegativeBignum), n.Bytes())
	}
	return AppendBytes(AppendTag(dst, TagPositiveBignum), n.Bytes())
}

// AppendDecimal appends the decimal fraction mantissa * 10^exp as tag 4, [exponent, mantissa] ; mantissa is
// written as an integer (see AppendInteger), so one with more than 19 digits becomes a bignum.
func AppendDecimal(dst []byte, mantissa string, exp int64) []byte {
	dst = AppendTag(dst, TagDecimal)
	dst = AppendArrayHead(dst, 2)
	dst = AppendInt(dst, exp)
	return AppendInteger(dst, mantissa)
}

// AppendEpoch appends an instant as tag 1 : integer seconds when there is no fraction, and a double of
// seconds otherwise.
func AppendEpoch(dst []byte, sec int64, micros int) []byte {
	dst = AppendTag(dst, TagEpochDateTime)
	if micros == 0 {
		return AppendInt(dst, sec)
	}
	return AppendFloat64(dst, float64(sec)+float64(micros)/1e6)
}

// AppendUUID appends a UUID as tag 37 over its 16 bytes.
func AppendUUID(dst []byte, uuid [16]byte) []byte {
	return AppendBytes(AppendTag(dst, TagUUID), uuid[:])
}

// AppendEpochDate appends a date as tag 100 : days since 1970-01-01.
func AppendEpochDate(dst []byte, days int64) []byte {
	return AppendInt(AppendTag(dst, TagEpochDate), days)
}
