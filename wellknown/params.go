package wellknown

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/query"
	"github.com/samber/oops"
)

// ResolveParams validates and defaults a request's "params" object against c.Params, returning one value per
// declared param (query.ParamValue). params may be nil or a null. A key not declared in c.Params is ignored.
func (c *Compiled) ResolveParams(params query.Node) (map[string]any, error) {
	if params != nil && params.Kind() != query.KindObject && params.Kind() != query.KindNull {
		return nil, oops.Code(errcode.QueryMalformedJSON).Errorf("wellknown: params: expected an object, got a %s", params.Kind())
	}
	out := make(map[string]any, len(c.Params))
	for name, def := range c.Params {
		var supplied query.Node
		found := false
		if params != nil && params.Kind() == query.KindObject {
			supplied, found = params.ObjectGet(name)
		}
		if !found {
			if !def.HasDefault {
				return nil, oops.With("param", name).Code(errcode.WellKnownParamRequired).Errorf("wellknown: param %q is required", name)
			}
			var v any
			if err := json.Unmarshal(def.Default, &v); err != nil {
				return nil, oops.With("param", name).Code(errcode.WellKnownParamTypeMismatch).Errorf("wellknown: param %q: invalid default: %w", name, err)
			}
			out[name] = v
			continue
		}
		if query.IsCBORTypeName(def.Type) {
			v, err := query.CBORParamValue(supplied)
			if err != nil {
				return nil, oops.With("param", name).Code(errcode.WellKnownParamTypeMismatch).Errorf("wellknown: param %q: %w", name, err)
			}
			out[name] = v
			continue
		}
		if err := checkParamType(def.Type, supplied.Kind()); err != nil {
			return nil, oops.With("param", name).Code(errcode.WellKnownParamTypeMismatch).Errorf("wellknown: param %q: %w", name, err)
		}
		v, err := query.ParamValue(supplied, isTimestamp(def.Type))
		if err != nil {
			return nil, oops.With("param", name).Code(errcode.WellKnownParamTypeMismatch).Errorf("wellknown: param %q: invalid value: %w", name, err)
		}
		out[name] = v
	}
	return out, nil
}

// checkParamType is a shallow, kind-level front door ; a null always passes, an unrecognized type defers to the
// SQL cast. A date and time, date or UUID that only CBOR carries stands for the string of its type.
func checkParamType(pgType string, kind query.NodeKind) error {
	if kind == query.KindNull {
		return nil
	}
	t := strings.ToLower(strings.TrimSpace(pgType))
	var want query.NodeKind
	switch classifyPgType(t) {
	case pgTypeNumeric:
		want = query.KindNumber
	case pgTypeText:
		want = query.KindString
		if k, ok := cborKindOf[t]; ok && kind == k {
			return nil
		}
	case pgTypeBoolean:
		want = query.KindBool
	default:
		return nil
	}
	if kind != want {
		return oops.Errorf("expected a %s for postgres type %q, got a %s", want, pgType, kind)
	}
	return nil
}

// cborKindOf is the CBOR-only kind each text-class type also takes.
var cborKindOf = map[string]query.NodeKind{
	"uuid": query.KindUUID, "date": query.KindDate, "timestamp": query.KindInstant, "timestamptz": query.KindInstant,
}

// isTimestamp reports a timestamp without time zone, which reads a CBOR date and time in the session zone.
func isTimestamp(pgType string) bool {
	t := strings.ToLower(strings.TrimSpace(pgType))
	return t == "timestamp" || t == "timestamp without time zone"
}

// jsonKindOf sniffs raw's outermost JSON kind from its first non-whitespace byte.
func jsonKindOf(raw []byte) query.NodeKind {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return query.KindInvalid
	}
	switch trimmed[0] {
	case 'n':
		return query.KindNull
	case 't', 'f':
		return query.KindBool
	case '"':
		return query.KindString
	case '{':
		return query.KindObject
	case '[':
		return query.KindArray
	default:
		return query.KindNumber
	}
}

type pgTypeClass int

const (
	pgTypeOther pgTypeClass = iota
	pgTypeNumeric
	pgTypeText
	pgTypeBoolean
)

var numericPgTypes = map[string]bool{
	"int": true, "int2": true, "int4": true, "int8": true,
	"integer": true, "smallint": true, "bigint": true,
	"numeric": true, "decimal": true,
	"real": true, "float4": true, "float8": true, "double precision": true,
	"money": true, "oid": true, "serial": true, "bigserial": true, "smallserial": true,
}

var textPgTypes = map[string]bool{
	"text": true, "varchar": true, "character varying": true,
	"char": true, "character": true, "bpchar": true, "name": true, "citext": true,
	"uuid": true, "date": true, "time": true, "timetz": true,
	"timestamp": true, "timestamptz": true, "interval": true,
}

var booleanPgTypes = map[string]bool{"boolean": true, "bool": true}

// TSTypeForParam maps a declared param's "type" to its TypeScript type, from the same buckets checkParamType
// accepts.
func TSTypeForParam(pgType string) string {
	switch classifyPgType(pgType) {
	case pgTypeNumeric:
		return "number"
	case pgTypeText:
		return "string"
	case pgTypeBoolean:
		return "boolean"
	default:
		return "unknown"
	}
}

// paramWireMembers are the declared types whose JS value depends on the codec : the PgWire member that types the
// value, and its type with no codec option.
var paramWireMembers = map[string][2]string{
	"int8": {"int8", "number"}, "bigint": {"int8", "number"}, "bigserial": {"int8", "number"},
	"numeric": {"numeric", "number"}, "decimal": {"numeric", "number"},
	"timestamptz": {"timestamptz", "string"}, "timestamp": {"timestamp", "string"}, "date": {"date", "string"},
	"bytea": {"bytea", "string"},
}

// TSWireTypeForParam is the TypeScript type a client passes for a declared param : TSTypeForParam, or for a type
// whose JS value depends on the codec, the same unbranded PgWire type its columns read as (a Date for a
// timestamptz with `cbor`, a Uint8Array for a bytea...).
func TSWireTypeForParam(pgType string) string {
	if m, ok := paramWireMembers[strings.ToLower(strings.TrimSpace(pgType))]; ok {
		return fmt.Sprintf("PgWireType<%q, %s>", m[0], m[1])
	}
	return TSTypeForParam(pgType)
}

func classifyPgType(pgType string) pgTypeClass {
	t := strings.ToLower(strings.TrimSpace(pgType))
	switch {
	case numericPgTypes[t]:
		return pgTypeNumeric
	case textPgTypes[t]:
		return pgTypeText
	case booleanPgTypes[t]:
		return pgTypeBoolean
	default:
		return pgTypeOther
	}
}
