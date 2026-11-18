package query

import (
	"encoding/hex"
	"encoding/json"

	"github.com/samber/oops"
)

// sessionInstant is a well-known param's CBOR date and time, written as text by BindSessionParams once the session
// zone is known. wallClock marks a param declared as a timestamp, which a zone rel cannot load refuses.
type sessionInstant struct {
	micros    int64 // since 2000-01-01 UTC
	wallClock bool
}

// ParamValue is the value a well-known param binds for n : what encoding/json decodes from its JSON text, or for a
// value only CBOR carries, the text a JSON client would send for it. A date and time is a placeholder until
// BindSessionParams ; wallClock marks a param declared as a timestamp.
func ParamValue(n Node, wallClock bool) (any, error) {
	switch n.Kind() {
	case KindInstant, KindDate, KindUUID, KindBytes:
		lit, err := parseTypedLiteral(n)
		if err != nil {
			return nil, err
		}
		if v, ok := lit.(InstantLiteral); ok {
			return sessionInstant{v.Micros, wallClock}, nil
		}
		return typedLiteralText(lit), nil
	}
	raw, err := n.Raw()
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, oops.Wrapf(err, "invalid value")
	}
	return v, nil
}

// typedLiteralText is the text a JSON client would send for a literal parseTypedLiteral gives, a date and time in UTC.
func typedLiteralText(lit Expression) string {
	switch v := lit.(type) {
	case InstantLiteral:
		return pgInstantText(v.Micros, nil)
	case DateLiteral:
		return pgDateText(int64(v.Days))
	case UUIDLiteral:
		return uuidText(v.Value[:])
	case BytesLiteral:
		return `\x` + hex.EncodeToString(v.Value)
	}
	return ""
}

// CBORBodyJSON reads a CBOR document as JSON text, for a route's req.body. A value only CBOR carries is the text a
// JSON client would send (typedLiteralText) ; what JSON cannot hold otherwise (a non-finite float, an unknown tag)
// is an error.
func CBORBodyJSON(data []byte) ([]byte, error) {
	n, err := NewCBORNode(data)
	if err != nil {
		return nil, err
	}
	return appendBodyJSON(nil, n)
}

func appendBodyJSON(dst []byte, n Node) ([]byte, error) {
	switch n.Kind() {
	case KindObject:
		dst = append(dst, '{')
		first := true
		err := n.ObjectEach(func(k string, v Node) error {
			if !first {
				dst = append(dst, ',')
			}
			first = false
			kb, err := json.Marshal(k)
			if err != nil {
				return err
			}
			dst = append(append(dst, kb...), ':')
			dst, err = appendBodyJSON(dst, v)
			return err
		})
		return append(dst, '}'), err
	case KindArray:
		items, err := n.ArrayItems()
		if err != nil {
			return nil, err
		}
		dst = append(dst, '[')
		for i, it := range items {
			if i > 0 {
				dst = append(dst, ',')
			}
			if dst, err = appendBodyJSON(dst, it); err != nil {
				return nil, err
			}
		}
		return append(dst, ']'), nil
	case KindInstant, KindDate, KindUUID, KindBytes:
		lit, err := parseTypedLiteral(n)
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(typedLiteralText(lit))
		return append(dst, b...), err
	}
	raw, err := nodeJSON(n)
	if err != nil {
		return nil, err
	}
	return append(dst, raw...), nil
}

// BindSessionParams writes each date and time ParamValue left as the wall clock and offset of q's session zone : the
// same instant through a timestamptz cast, and that zone's wall clock through a timestamp one, as a write's data
// reads it. When rel cannot load the zone, a param declared as a timestamp is an error and any other is UTC.
func BindSessionParams(q Querier, values map[string]any) error {
	var ec *encodeContext
	for name, v := range values {
		si, ok := v.(sessionInstant)
		if !ok {
			continue
		}
		if ec == nil {
			ec = encodeContextFor(q)
		}
		if ec.zoneErr != nil {
			if si.wallClock {
				return oops.With("param", name).Wrap(errUnknownZone("timestamp", pgInstantText(si.micros, nil), ec.zoneName))
			}
			values[name] = pgInstantText(si.micros, nil)
			continue
		}
		values[name] = pgInstantText(si.micros, ec.location())
	}
	return nil
}
