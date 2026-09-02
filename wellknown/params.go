package wellknown

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ceymard/rel/errcode"
	"github.com/samber/oops"
)

// ResolveParams validates and defaults one request's own "params" object
// against c's declared Params (specs/well-known-queries.md ## Definition),
// returning one Go value per declared param — ready to hand straight to
// writer.SQLWriter.ResolveArgs (the read path) or
// query.ExecuteWriteStateParams (the write path). raw is the caller's own
// "params" JSON object verbatim, or nil/empty if the request omitted it
// entirely (equivalent to every declared param being absent).
//
// A key present in raw but not declared in c.Params is silently ignored —
// specs/well-known-queries.md's own error taxonomy only defines
// WELL_KNOWN_UNKNOWN_PARAM for a *query* referencing an undeclared param at
// load time (## Compilation Errors), not for a caller sending an unused
// extra key ; there's nothing unsafe about ignoring it.
func (c *Compiled) ResolveParams(raw []byte) (map[string]any, error) {
	supplied := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &supplied); err != nil {
			return nil, oops.Code(errcode.QueryMalformedJSON).Errorf("wellknown: params: invalid JSON: %w", err)
		}
	}

	out := make(map[string]any, len(c.Params))
	for name, def := range c.Params {
		rawVal, ok := supplied[name]
		if !ok {
			if !def.HasDefault {
				return nil, oops.With("param", name).Code(errcode.WellKnownParamRequired).Errorf("wellknown: param %q is required", name)
			}
			rawVal = def.Default
		} else if err := checkParamType(def.Type, rawVal); err != nil {
			return nil, oops.With("param", name).Code(errcode.WellKnownParamTypeMismatch).Errorf("wellknown: param %q: %w", name, err)
		}

		var v any
		if err := json.Unmarshal(rawVal, &v); err != nil {
			return nil, oops.With("param", name).Code(errcode.WellKnownParamTypeMismatch).Errorf("wellknown: param %q: invalid value: %w", name, err)
		}
		out[name] = v
	}
	return out, nil
}

// checkParamType is a deliberately shallow, JSON-kind-level front door —
// specs/well-known-queries.md ## Definition : "type is checked against the
// caller-supplied JSON value up front, in Go, before the query ever
// reaches Postgres". It buckets a handful of common Postgres type spellings
// into "must be a JSON number/string/boolean", and defers everything else
// (an unset type, jsonb/json, an unrecognized/exotic type name) to the
// actual SQL-side ::cast written at the $param's own usage site
// (sql_expr.go's ParamExpr case) — this is not a Postgres type system, just
// enough to catch the routine mistake of sending a string where a number
// was declared. A JSON null always passes, for any declared type : SQL NULL
// is valid for any column regardless of its type.
func checkParamType(pgType string, raw json.RawMessage) error {
	kind := jsonKindOf(raw)
	if kind == jsonNull {
		return nil
	}
	switch classifyPgType(pgType) {
	case pgTypeNumeric:
		if kind != jsonNumber {
			return fmt.Errorf("expected a JSON number for postgres type %q, got %s", pgType, kind)
		}
	case pgTypeText:
		if kind != jsonString {
			return fmt.Errorf("expected a JSON string for postgres type %q, got %s", pgType, kind)
		}
	case pgTypeBoolean:
		if kind != jsonBool {
			return fmt.Errorf("expected a JSON boolean for postgres type %q, got %s", pgType, kind)
		}
	}
	return nil
}

type jsonKind int

const (
	jsonNull jsonKind = iota
	jsonBool
	jsonNumber
	jsonString
	jsonOther
)

func (k jsonKind) String() string {
	switch k {
	case jsonNull:
		return "null"
	case jsonBool:
		return "a boolean"
	case jsonNumber:
		return "a number"
	case jsonString:
		return "a string"
	default:
		return "an object/array"
	}
}

// jsonKindOf sniffs raw's outermost JSON kind from its first non-whitespace
// byte — cheap and sufficient here, since the only thing checkParamType
// needs is the coarse bucket, not a full decode.
func jsonKindOf(raw json.RawMessage) jsonKind {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return jsonOther
	}
	switch trimmed[0] {
	case 'n':
		return jsonNull
	case 't', 'f':
		return jsonBool
	case '"':
		return jsonString
	case '{', '[':
		return jsonOther
	default:
		return jsonNumber
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
