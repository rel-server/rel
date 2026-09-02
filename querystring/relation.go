// Copyright 2025 Christophe Eymard
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package querystring

import (
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/samber/oops"
)

// relationFields is every query.ts Relation key this package understands,
// each mapped to how its structurally-decoded value (a string, from
// DecodeStructural — nested maps only ever occur for "on" and "join")
// compiles to the final Relation JSON. Fields not listed here (an unknown
// query key) are a decode error — specs/query-json.md never says GET /rel
// should silently ignore an unrecognized key, and erroring catches typos
// (e.g. "order-by" instead of "order_by") that would otherwise silently no-op.
type fieldKind int

const (
	kindPlainString   fieldKind = iota // relation, function, schema, alias
	kindOnMap                          // on : {local_column: parent_column}
	kindExpr                           // where : one full `expr`
	kindSelect                         // select : own/full/comma-list, see compileSelect
	kindCommaExprList                  // distinct_on : comma list of `expr`
	kindOrderByList                    // order_by : comma list, each optionally "-"-prefixed
	kindInt                            // limit, offset
	kindBool                           // distinct
	kindStringOrList                   // on_conflict : string | string[]
	kindStringList                     // insert_columns, update_columns
	kindWriteMode                      // write_mode : plain string, GET-forbidden (see rejectGetOnlyFields)
	kindJoin                           // join : {alias: Relation}, recurses
	kindArguments                      // arguments : positional or named, see compileArguments
)

var relationFieldKinds = map[string]fieldKind{
	"relation":       kindPlainString,
	"function":       kindPlainString,
	"schema":         kindPlainString,
	"alias":          kindPlainString,
	"on":             kindOnMap,
	"where":          kindExpr,
	"select":         kindSelect,
	"distinct_on":    kindCommaExprList,
	"order_by":       kindOrderByList,
	"limit":          kindInt,
	"offset":         kindInt,
	"distinct":       kindBool,
	"on_conflict":    kindStringOrList,
	"insert_columns": kindStringList,
	"update_columns": kindStringList,
	"write_mode":     kindWriteMode,
	"join":           kindJoin,
	"arguments":      kindArguments,
}

// getOnlyForbiddenFields is specs/query-json.md's ## Scope list, rejected
// anywhere in the decoded tree (root or any depth of join) on GET /rel.
var getOnlyForbiddenFields = map[string]bool{
	"write_mode":     true,
	"on_conflict":    true,
	"insert_columns": true,
	"update_columns": true,
}

// DecodeRelation implements specs/query-json.md end to end for GET /rel :
// raw query string -> structural decode -> per-field compile (expression
// grammar for where/select/order_by/distinct_on/arguments, own/full for
// select, dot-path recursion for join) -> the read-only/single-relation
// restriction check -> a plain Go value tree ready for sonic.Marshal into
// bytes query.ParseQuery accepts unchanged.
func DecodeRelation(raw string) ([]byte, error) {
	tree, err := DecodeStructural(raw)
	if err != nil {
		return nil, oops.Wrapf(err, "decoding query string")
	}
	compiled, err := compileRelation(tree)
	if err != nil {
		return nil, oops.Wrapf(err, "compiling query string")
	}
	if err := rejectGetOnlyFields(compiled); err != nil {
		return nil, err
	}
	out, err := sonic.Marshal(compiled)
	if err != nil {
		return nil, oops.Wrapf(err, "marshaling decoded query")
	}
	return out, nil
}

// DecodeQueryField implements /route's `query` field : the structural layer
// only (no filter expression grammar involvement — specs/query-json.md's
// own ## /route's query field section), generalized to any shape, not scoped
// to Relation's fixed keys. Returns nil (encodes as JSON null) for an empty
// raw query string, matching "no query string at all" rather than "an empty
// object" — a route function distinguishing the two is a reasonable thing
// to want and costs nothing to preserve.
func DecodeQueryField(raw string) (any, error) {
	trimmed := strings.TrimPrefix(raw, "?")
	if trimmed == "" {
		return nil, nil
	}
	tree, err := DecodeStructural(raw)
	if err != nil {
		return nil, oops.Wrapf(err, "decoding query string")
	}
	return tree, nil
}

// compileRelation walks one structurally-decoded node (a map[string]any
// whose values are strings/[]any-of-strings/nested maps, per
// DecodeStructural) and compiles it into query.ts's Relation JSON shape.
func compileRelation(node map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for key, raw := range node {
		kind, ok := relationFieldKinds[key]
		if !ok {
			return nil, oops.Errorf("unrecognized query key %q", key)
		}
		compiled, err := compileField(kind, key, raw)
		if err != nil {
			return nil, oops.Wrapf(err, "key %q", key)
		}
		out[key] = compiled
	}
	return out, nil
}

func compileField(kind fieldKind, key string, raw any) (any, error) {
	switch kind {
	case kindPlainString:
		return asString(raw)

	case kindWriteMode:
		return asString(raw)

	case kindOnMap:
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, oops.Errorf("expected a nested object (local_column.parent_column pairs)")
		}
		out := map[string]any{}
		for k, v := range m {
			s, err := asString(v)
			if err != nil {
				return nil, oops.Wrapf(err, "field %q", k)
			}
			out[k] = s
		}
		return out, nil

	case kindExpr:
		s, err := asString(raw)
		if err != nil {
			return nil, err
		}
		return parseFullExpr(s)

	case kindSelect:
		s, err := asString(raw)
		if err != nil {
			return nil, err
		}
		return compileSelect(s)

	case kindCommaExprList:
		s, err := asString(raw)
		if err != nil {
			return nil, err
		}
		return parseTopLevelExprList(s)

	case kindOrderByList:
		s, err := asString(raw)
		if err != nil {
			return nil, err
		}
		return compileOrderBy(s)

	case kindInt:
		s, err := asString(raw)
		if err != nil {
			return nil, err
		}
		v, cerr := strconv.Atoi(strings.TrimSpace(s))
		if cerr != nil {
			return nil, oops.Wrapf(cerr, "expected an integer, got %q", s)
		}
		return v, nil

	case kindBool:
		s, err := asString(raw)
		if err != nil {
			return nil, err
		}
		switch strings.TrimSpace(s) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		default:
			return nil, oops.Errorf("expected \"true\" or \"false\", got %q", s)
		}

	case kindStringOrList:
		switch v := raw.(type) {
		case string:
			return v, nil
		case []any:
			return stringSlice(v)
		default:
			return nil, oops.Errorf("expected a string or a repeated key")
		}

	case kindStringList:
		return stringSlice(toAnySlice(raw))

	case kindJoin:
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, oops.Errorf("expected a nested object ({alias: relation-fields})")
		}
		out := map[string]any{}
		for alias, child := range m {
			cm, ok := child.(map[string]any)
			if !ok {
				return nil, oops.Errorf("join %q: expected a nested object", alias)
			}
			compiled, err := compileRelation(cm)
			if err != nil {
				return nil, oops.Wrapf(err, "join %q", alias)
			}
			out[alias] = compiled
		}
		return out, nil

	case kindArguments:
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, oops.Errorf("expected a nested object (arguments.<name-or-index>=value)")
		}
		return compileArguments(m)

	default:
		return nil, oops.Errorf("internal error : unhandled field kind for %q", key)
	}
}

// asString requires raw to be a single (non-repeated) string value — every
// Relation field this package compiles other than "on"/"join"/"arguments"
// (nested objects) and comma-lists (which split their OWN internal commas,
// so the raw query key itself must not repeat) is exactly one query-string
// value.
func asString(raw any) (string, error) {
	s, ok := raw.(string)
	if !ok {
		return "", oops.Errorf("expected a single value, not a repeated key")
	}
	return s, nil
}

func toAnySlice(raw any) []any {
	switch v := raw.(type) {
	case []any:
		return v
	case string:
		return []any{v}
	default:
		return nil
	}
}

func stringSlice(items []any) ([]string, error) {
	out := make([]string, len(items))
	for i, it := range items {
		s, ok := it.(string)
		if !ok {
			return nil, oops.Errorf("item %d : expected a string", i)
		}
		out[i] = s
	}
	return out, nil
}

// compileArguments implements specs/query-json.md's `arguments.<key>=value`
// rule : each value is one filter-value TOKEN (an atom : identifier or
// literal — see ## Filter expression grammar's own atom production), never
// a full condition (`arguments.0=eq(status,'open')` is an error, per the
// spec's own worked note). All-digit sibling keys compile to the positional
// (array) form ; any other key set compiles to the named (object) form —
// this is a judgment call specs/query-json.md leaves implicit (see this
// session's report), chosen because query.ts's own "arguments" field is
// itself `Expression[] | {[name]: Expression}` and a set of purely numeric
// keys has no other sensible reading as anything but array indices.
// maxArgumentIndex bounds a positional "arguments.<n>" key : an attacker
// otherwise controls make([]any, n+1) directly off an unauthenticated GET
// query string (a single key like arguments.100000000 forces a ~1.6GB
// allocation ; an index past strconv.Atoi's int range forces a panic in
// make() once its error was silently ignored). No real query has anywhere
// near this many positional arguments, so this bound is purely a sanity
// cap, not a functional limit.
const maxArgumentIndex = 4096

func compileArguments(m map[string]any) (any, error) {
	allNumeric := len(m) > 0
	for k := range m {
		if !isAllDigits(k) {
			allNumeric = false
			break
		}
	}
	if allNumeric {
		maxIdx := -1
		for k := range m {
			n, err := strconv.Atoi(k)
			if err != nil || n < 0 || n > maxArgumentIndex {
				return nil, oops.Errorf("argument index %q out of range", k)
			}
			if n > maxIdx {
				maxIdx = n
			}
		}
		out := make([]any, maxIdx+1)
		for k, v := range m {
			n, _ := strconv.Atoi(k) // already validated above : in [0, maxArgumentIndex]
			s, err := asString(v)
			if err != nil {
				return nil, oops.Wrapf(err, "argument %s", k)
			}
			val, err := parseArgumentToken(s)
			if err != nil {
				return nil, oops.Wrapf(err, "argument %s", k)
			}
			out[n] = val
		}
		return out, nil
	}

	out := map[string]any{}
	for k, v := range m {
		s, err := asString(v)
		if err != nil {
			return nil, oops.Wrapf(err, "argument %q", k)
		}
		val, err := parseArgumentToken(s)
		if err != nil {
			return nil, oops.Wrapf(err, "argument %q", k)
		}
		out[k] = val
	}
	return out, nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

// parseArgumentToken parses one `arguments.*` value as a bare `atom` (never
// a `call`) — a plain identifier or a literal, per the spec's explicit
// "arguments are values, not conditions" rule.
func parseArgumentToken(s string) (any, error) {
	p := newExprParser(s)
	p.skipSpace()
	if p.i >= len(p.s) {
		return nil, oops.Errorf("empty argument value")
	}
	c := p.s[p.i]
	var v any
	var err error
	switch {
	case c == '\'':
		lit, lerr := p.parseStringLiteral()
		if lerr != nil {
			return nil, lerr
		}
		v, err = []any{lit}, nil
	case c == '-' || isDigit(c):
		v, err = p.parseNumber()
	case isIdentStart(c):
		ident, ierr := p.parseIdentifier()
		if ierr != nil {
			return nil, ierr
		}
		switch ident {
		case "true":
			v = true
		case "false":
			v = false
		case "null":
			v = nil
		default:
			if p.i < len(p.s) && p.s[p.i] == '(' {
				return nil, oops.Errorf("argument values must be an identifier or a literal, not a call : %q", s)
			}
			v = ident
		}
	default:
		return nil, oops.Errorf("unexpected character %q at position %d", c, p.i)
	}
	if err != nil {
		return nil, err
	}
	if !p.atEnd() {
		return nil, oops.Errorf("unexpected trailing input at position %d in %q", p.i, s)
	}
	return v, nil
}

// rejectGetOnlyFields walks the FULLY COMPILED Relation tree (never the raw
// query string keys — see specs/query-json.md's own note on why a key-name
// scan isn't equivalent) rejecting write_mode/on_conflict/insert_columns/
// update_columns at the root and at every nested join.<alias>, recursing
// only through "join" (a user-chosen select alias happening to be named
// "write_mode", e.g. select=write_mode:name, must not false-positive here —
// walking the decoded STRUCTURE, not scanning every map for a matching key,
// is what keeps that distinction intact).
func rejectGetOnlyFields(rel map[string]any) error {
	for field := range getOnlyForbiddenFields {
		if _, present := rel[field]; present {
			return oops.Errorf("%q is not valid on GET /rel (read-only, per specs/query-json.md ## Scope)", field)
		}
	}
	if joinRaw, ok := rel["join"]; ok {
		joinMap, ok := joinRaw.(map[string]any)
		if !ok {
			return nil
		}
		for alias, child := range joinMap {
			childMap, ok := child.(map[string]any)
			if !ok {
				continue
			}
			if err := rejectGetOnlyFields(childMap); err != nil {
				return oops.Wrapf(err, "join %q", alias)
			}
		}
	}
	return nil
}
