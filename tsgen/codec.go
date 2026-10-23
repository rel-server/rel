package tsgen

import (
	"strings"

	"github.com/samber/oops"
)

// Codec is the codec options of a generated database.ts : how requests and responses travel (Wire), what a date
// or time type is on the client (Time) and whether an int8 keeps every digit (BigInt). The zero value is the plain
// JSON file with no import. docs/content/typescript/index.md ## Codecs.
type Codec struct {
	// Wire is "json" (also ""), or "cbor".
	Wire string
	// Time is "" (text), "date" or "temporal".
	Time string
	// BigInt makes int8 a `number | bigint`.
	BigInt bool
}

// ParseCodec reads the comma-separated list of tokens of `typescript.codec` or the `codec` query param :
// `json`|`cbor`, `date`|`temporal` and `bigint`, in any order. Blank items are ignored ; "" is the zero Codec.
// `cbor` implies `bigint`, and `date` when no time token is given. A token twice, two tokens of one group
// (`json,cbor`) and an unknown token are errors.
func ParseCodec(s string) (Codec, error) {
	var c Codec
	seen := map[string]bool{}
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if seen[tok] {
			return Codec{}, oops.With("codec", s).Errorf("codec: %q is given twice", tok)
		}
		seen[tok] = true
		switch tok {
		case "json", "cbor":
			if c.Wire != "" {
				return Codec{}, oops.With("codec", s).Errorf("codec: %q and %q both set the wire format", c.Wire, tok)
			}
			c.Wire = tok
		case "date", "temporal":
			if c.Time != "" {
				return Codec{}, oops.With("codec", s).Errorf("codec: %q and %q both set the date and time type", c.Time, tok)
			}
			c.Time = tok
		case "bigint":
			c.BigInt = true
		default:
			return Codec{}, oops.With("codec", s).Errorf("codec: unknown token %q (known: json, cbor, date, temporal, bigint)", tok)
		}
	}
	return c.normalized(), nil
}

// normalized fills in what the wire format implies : cbor-x reads an 8-byte integer as a bigint and the server
// sends a timestamp as a CBOR tag, so `cbor` always means bigint and a time type.
func (c Codec) normalized() Codec {
	if c.Wire == "" {
		c.Wire = "json"
	}
	if c.Wire == "cbor" {
		c.BigInt = true
		if c.Time == "" {
			c.Time = "date"
		}
	}
	return c
}

// String is the canonical token list of c, as written in the generated file's header : ParseCodec(c.String())
// is c.
func (c Codec) String() string {
	c = c.normalized()
	parts := []string{c.Wire}
	if c.Time != "" {
		parts = append(parts, c.Time)
	}
	if c.BigInt {
		parts = append(parts, "bigint")
	}
	return strings.Join(parts, ",")
}

// chunks lists the files of typescript/codecs that c needs, in the order they are appended to the generated file :
// a chunk fills tables an earlier one defines (the tag chunks fill wire-cbor's).
func (c Codec) chunks() []string {
	c = c.normalized()
	var out []string
	switch c.Time {
	case "":
		out = append(out, "time-text.ts")
	default:
		out = append(out, "time-"+c.Time+".ts")
	}
	switch {
	case c.Wire == "cbor":
		out = append(out, "wire-cbor.ts", "cbor-time-"+c.Time+".ts")
	case c.BigInt:
		out = append(out, "json-bigint.ts")
	}
	return out
}

// pgWireMembers lists the `PgWire` members c retypes, as TypeScript source lines : what the values the codec
// hands over are, for the types a plain JSON.parse leaves as numbers, text or hex.
func (c Codec) pgWireMembers() []string {
	c = c.normalized()
	var out []string
	if c.BigInt {
		out = append(out, "int8: number | bigint")
	}
	if c.Wire != "cbor" {
		// JSON leaves every date, time and bytea as text ; the time token only changes the accessors.
		return out
	}
	out = append(out, "bytea: Uint8Array")
	if c.Time == "temporal" {
		out = append(out, "timestamptz: Temporal.Instant", "timestamp: Temporal.Instant", "date: Temporal.PlainDate")
	} else {
		out = append(out, "timestamptz: Date", "timestamp: Date")
	}
	return out
}
