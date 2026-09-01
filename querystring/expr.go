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

// Filter expression grammar : specs/query-json.md's ## Filter expression
// grammar, a small S-expression-style parser producing the SAME plain Go
// value tree shape query.ts's own JSON Expression uses (string, float64,
// bool, nil, []any, map[string]any) — never a parallel Expression type.
// This one recursive-descent scanner is reused for `where`, every
// select/order_by/distinct_on comma-list, and each `call`'s own argument
// list, per the spec's own "one tokenizer, reused everywhere" rule.
package querystring

import (
	"strconv"
	"strings"

	"github.com/ceymard/rel/query"
	"github.com/samber/oops"
)

// exprParser is a single left-to-right scanner over one query-string value
// (already percent-decoded). Every list/call/alias production below walks
// the SAME p.i cursor, so a literal or nested call's own commas/colons/
// parens can never be mistaken for a delimiter at the wrong nesting level —
// this is what "the same quote-and-paren-aware scanner" means in practice.
type exprParser struct {
	s string
	i int
}

func newExprParser(s string) *exprParser { return &exprParser{s: s} }

// querystringOnlyKeywords covers call identifiers that specs/query-json.md
// spells with the SAME word both here and in the underlying query.ts tag
// (so they need no entry in query.OperatorWords — that table exists for
// pass-1 JSON's word-form SYNONYM feature, and these tags never had a
// second, symbolic spelling to be a synonym of) but that this grammar still
// needs to dispatch on specially rather than let fall through to the
// generic "unrecognized identifier -> plain function call" case : bigint/
// numeric's argument is a raw string, not an Expression, and the six own/
// full-family calls are select-only, rejected here as a sub-expression.
var querystringOnlyKeywords = map[string]string{
	"bigint":          "bigint",
	"numeric":         "numeric",
	"own_except":      "own_except",
	"full_except":     "full_except",
	"own_and":         "own_and",
	"full_and":        "full_and",
	"own_except_and":  "own_except_and",
	"full_except_and": "full_except_and",
}

func (p *exprParser) skipSpace() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

func (p *exprParser) atEnd() bool {
	p.skipSpace()
	return p.i >= len(p.s)
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// parseIdentifier consumes specs/query-json.md's `identifier` production :
// /[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*/ — e.g. "actors.name".
func (p *exprParser) parseIdentifier() (string, error) {
	start := p.i
	if p.i >= len(p.s) || !isIdentStart(p.s[p.i]) {
		return "", oops.Errorf("expected an identifier at position %d in %q", p.i, p.s)
	}
	p.i++
	for p.i < len(p.s) && isIdentPart(p.s[p.i]) {
		p.i++
	}
	for p.i < len(p.s) && p.s[p.i] == '.' && p.i+1 < len(p.s) && isIdentStart(p.s[p.i+1]) {
		p.i++ // consume '.'
		p.i++ // consume the identifier-start char just checked for
		for p.i < len(p.s) && isIdentPart(p.s[p.i]) {
			p.i++
		}
	}
	return p.s[start:p.i], nil
}

// parseStringLiteral consumes a 'quoted string' — ” is a literal single
// quote (SQL-style doubling, per the spec's own quoting-rule rationale).
// Returns the raw (already-unquoted/unescaped) string content.
func (p *exprParser) parseStringLiteral() (string, error) {
	if p.i >= len(p.s) || p.s[p.i] != '\'' {
		return "", oops.Errorf("expected a quoted string at position %d in %q", p.i, p.s)
	}
	p.i++
	var b strings.Builder
	for {
		if p.i >= len(p.s) {
			return "", oops.Errorf("unterminated string literal in %q", p.s)
		}
		c := p.s[p.i]
		if c == '\'' {
			if p.i+1 < len(p.s) && p.s[p.i+1] == '\'' {
				b.WriteByte('\'')
				p.i += 2
				continue
			}
			p.i++
			return b.String(), nil
		}
		b.WriteByte(c)
		p.i++
	}
}

// parseNumber consumes -?[0-9]+(\.[0-9]+)? and returns it as float64,
// matching query.ts's own Expression `number` form (a bare JSON number).
func (p *exprParser) parseNumber() (float64, error) {
	start := p.i
	if p.i < len(p.s) && p.s[p.i] == '-' {
		p.i++
	}
	digitsStart := p.i
	for p.i < len(p.s) && isDigit(p.s[p.i]) {
		p.i++
	}
	if p.i == digitsStart {
		return 0, oops.Errorf("expected a number at position %d in %q", start, p.s)
	}
	if p.i < len(p.s) && p.s[p.i] == '.' && p.i+1 < len(p.s) && isDigit(p.s[p.i+1]) {
		p.i++
		for p.i < len(p.s) && isDigit(p.s[p.i]) {
			p.i++
		}
	}
	v, err := strconv.ParseFloat(p.s[start:p.i], 64)
	if err != nil {
		return 0, oops.Wrapf(err, "invalid number %q", p.s[start:p.i])
	}
	return v, nil
}

// parseExpr parses one `expr` (call | atom) per the grammar, returning the
// query.ts JSON-shaped value it compiles to : a bare identifier compiles to
// a plain string (column/alias reference), a quoted literal to a
// one-element []any (StringLiteral form), a number/bool/null to the
// matching JSON scalar, and a call to []any{tag, args...} — tag already
// normalized to query.ts's own canonical operator spelling via
// query.OperatorWords, or left as-is (an unrecognized identifier) to
// compile as a plain function call below.
func (p *exprParser) parseExpr() (any, error) {
	p.skipSpace()
	if p.i >= len(p.s) {
		return nil, oops.Errorf("unexpected end of expression in %q", p.s)
	}
	c := p.s[p.i]
	switch {
	case c == '\'':
		lit, err := p.parseStringLiteral()
		if err != nil {
			return nil, err
		}
		return []any{lit}, nil

	case c == '-' || isDigit(c):
		// A leading '-' always opens a negative number here : identifiers
		// never start with '-' or a digit (see the grammar's own note on
		// why this is unambiguous), so there is no operator-name case to
		// consider at this position.
		return p.parseNumber()

	case isIdentStart(c):
		ident, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.i < len(p.s) && p.s[p.i] == '(' {
			return p.parseCall(ident)
		}
		switch ident {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null":
			return nil, nil
		}
		return ident, nil

	default:
		return nil, oops.Errorf("unexpected character %q at position %d in %q", c, p.i, p.s)
	}
}

// parseCall consumes "(" [expr ("," expr)*] ")" (the cursor is already
// positioned right at "(") and compiles ident(args...) to its query.ts
// array form. Special-cased tags whose JSON shape isn't a plain
// [tag, ...compiledArgs] : between/not_between, bigint/numeric, in/not_in
// (candidates are literals, not sub-expressions), agg/call (FunctionRef
// first argument), own/full and family (rejected here — see relation.go's
// doc comment on why they're select-only, not a sub-expression form).
func (p *exprParser) parseCall(ident string) (any, error) {
	p.i++ // consume '('
	args, err := p.parseArgList()
	if err != nil {
		return nil, err
	}

	canonical, known := query.OperatorWords[ident]
	if !known {
		canonical, known = querystringOnlyKeywords[ident], querystringOnlyKeywords[ident] != ""
	}
	if !known {
		// Not a known operator/keyword word : per specs/query-json.md,
		// `call`'s identifier "names an operator/function exactly as
		// query.ts's ... aggregate or function name would" — an
		// unrecognized name is a plain function call, query.ts's own
		// ["call", identifier, ...arguments] form. The identifier itself
		// (possibly dotted, e.g. "pg_catalog.lower") compiles the same way
		// agg/call's own explicit FunctionIdentifier argument does.
		fnRef, err := functionRefFromName(ident)
		if err != nil {
			return nil, err
		}
		out := []any{"call", fnRef}
		for _, a := range args {
			out = append(out, a.value())
		}
		return out, nil
	}

	switch canonical {
	case "own", "full", "own_except", "full_except", "own_and", "full_and", "own_except_and", "full_except_and":
		return nil, oops.Errorf("%q is only valid as select='s entire value, not inside another expression", ident)

	case "any", "all":
		if len(args) != 3 {
			return nil, oops.Errorf("%q needs exactly 3 operands (operator, subject, array), got %d", ident, len(args))
		}
		opArg, ok := args[0].(plainArg)
		if !ok {
			return nil, oops.Errorf("%q's operator must be a bare operator word, not a quoted string", ident)
		}
		opStr, ok := opArg.v.(string)
		if !ok {
			return nil, oops.Errorf("%q's operator must be a bare operator word", ident)
		}
		if opCanonical, ok := query.OperatorWords[opStr]; ok {
			opStr = opCanonical
		}
		return []any{canonical, opStr, args[1].value(), args[2].value()}, nil

	case "bigint", "numeric":
		if len(args) != 1 {
			return nil, oops.Errorf("%q needs exactly one quoted string argument, got %d", ident, len(args))
		}
		s, ok := args[0].(literalString)
		if !ok {
			return nil, oops.Errorf("%q's argument must be a quoted string", ident)
		}
		return []any{canonical, string(s)}, nil

	case "in", "not_in":
		if len(args) < 1 {
			return nil, oops.Errorf("%q needs a subject and at least one candidate", ident)
		}
		out := []any{canonical, args[0].value()}
		for _, a := range args[1:] {
			lit, ok := a.(literalString)
			if !ok {
				// Numbers/bool/null already decode to their own bare JSON
				// scalar via parseExpr, matching query.ts's own candidate
				// type (string | Expression) — only a bare, unquoted
				// IDENTIFIER candidate is rejected here (it decoded to a
				// plain Go string, meaning "column reference", which the
				// spec explicitly disallows for in/not_in candidates).
				if pa, isPlain := a.(plainArg); isPlain {
					if s, isIdent := pa.v.(string); isIdent {
						return nil, oops.Errorf("%q candidate %q must be a quoted literal, not a bare identifier", ident, s)
					}
				}
				out = append(out, a.value())
				continue
			}
			out = append(out, string(lit))
		}
		return out, nil

	case "format":
		// query.ts's ["format", format: string, ...Expression[]] : unlike
		// every other call form, the format string itself is a raw string
		// field, not an Expression — so it must compile to a bare string,
		// same special-casing as bigint/numeric's sole argument.
		if len(args) < 1 {
			return nil, oops.Errorf("%q needs a format string", ident)
		}
		lit, ok := args[0].(literalString)
		if !ok {
			return nil, oops.Errorf("%q's format string must be a quoted literal", ident)
		}
		out := []any{"format", string(lit)}
		for _, a := range args[1:] {
			out = append(out, a.value())
		}
		return out, nil

	case "agg":
		if len(args) < 2 {
			return nil, oops.Errorf("%q needs a function identifier and at least one argument", ident)
		}
		fnRef, err := functionRefFromArg(args[0])
		if err != nil {
			return nil, oops.Wrapf(err, "%q identifier", ident)
		}
		rest := make([]any, len(args)-1)
		for i, a := range args[1:] {
			rest[i] = a.value()
		}
		return []any{"agg", fnRef, rest}, nil

	case "call":
		if len(args) < 1 {
			return nil, oops.Errorf("%q needs a function identifier", ident)
		}
		fnRef, err := functionRefFromArg(args[0])
		if err != nil {
			return nil, oops.Wrapf(err, "%q identifier", ident)
		}
		out := []any{"call", fnRef}
		for _, a := range args[1:] {
			out = append(out, a.value())
		}
		return out, nil

	default:
		// "-" and "~" each cover two query.ts operators of different arity
		// (UnaryOperator vs. FoldedOperator/BinaryOperator) — see the word
		// table's own note in specs/query-json.md : "each gets its own
		// distinct word... so the collision doesn't carry over into this
		// grammar at all." That guarantee only holds if the word chosen is
		// actually checked against the arity it claims ; without this, e.g.
		// neg(5,3) would silently compile to ["-",5,3] (subtraction) instead
		// of rejecting the mismatched word.
		switch ident {
		case "neg", "bnot":
			if len(args) != 1 {
				return nil, oops.Errorf("%q takes exactly 1 argument, got %d", ident, len(args))
			}
		case "match":
			if len(args) != 2 {
				return nil, oops.Errorf("%q takes exactly 2 arguments, got %d", ident, len(args))
			}
		case "sub":
			if len(args) < 2 {
				return nil, oops.Errorf("%q takes at least 2 arguments (use neg for unary negation), got %d", ident, len(args))
			}
		}
		out := make([]any, len(args)+1)
		out[0] = canonical
		for i, a := range args {
			out[i+1] = a.value()
		}
		return out, nil
	}
}

// literalString marks an argument that was written as a quoted string
// literal, as opposed to a bare identifier that happens to also be a Go
// string once compiled (parseExpr's identifier case) — callers needing to
// tell "the user wrote 'x'" from "the user wrote x" (in/not_in candidates,
// bigint/numeric's argument) switch on this wrapper type rather than on the
// compiled value's own Go type, which is identical (string) in both cases.
type literalString string

// callArg is one already-parsed call argument, keeping enough of its own
// syntactic shape (was it a quoted literal?) for the few call forms that
// care, alongside its already-compiled query.ts JSON value.
type callArg interface{ value() any }

func (s literalString) value() any { return []any{string(s)} }

// identArg / plainArg wrap an ordinary compiled value (identifier,
// call-result, number, bool, null) — value() is the value itself, no
// literal-vs-identifier distinction needed by ordinary callers.
type plainArg struct{ v any }

func (a plainArg) value() any { return a.v }

// parseArgList consumes [expr ("," expr)*] ")" — the cursor is positioned
// right after the call's opening '('. Reuses parseExpr for each argument :
// the exact same recursive-descent path parseExpr itself calls into for a
// nested call, so there is only ever one comma-consuming loop in this
// package.
func (p *exprParser) parseArgList() ([]callArg, error) {
	var out []callArg
	p.skipSpace()
	if p.i < len(p.s) && p.s[p.i] == ')' {
		p.i++
		return out, nil
	}
	for {
		p.skipSpace()
		var arg callArg
		if p.i < len(p.s) && p.s[p.i] == '\'' {
			lit, err := p.parseStringLiteral()
			if err != nil {
				return nil, err
			}
			arg = literalString(lit)
		} else {
			v, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			arg = plainArg{v}
		}
		out = append(out, arg)
		p.skipSpace()
		if p.i >= len(p.s) {
			return nil, oops.Errorf("unterminated call, expected ',' or ')' in %q", p.s)
		}
		switch p.s[p.i] {
		case ',':
			p.i++
			continue
		case ')':
			p.i++
			return out, nil
		default:
			return nil, oops.Errorf("expected ',' or ')' at position %d in %q", p.i, p.s)
		}
	}
}

// functionRefFromArg compiles agg/call's own first argument — a
// FunctionIdentifier, written as a dotted identifier (schema-qualified,
// -> {schema, name}) or a bare one (-> a plain string, resolved via the
// search path), per query.ts's own two JSON forms.
func functionRefFromArg(a callArg) (any, error) {
	pa, ok := a.(plainArg)
	if !ok {
		return nil, oops.Errorf("must be a function identifier (schema.name or name), not a quoted string")
	}
	name, ok := pa.v.(string)
	if !ok {
		return nil, oops.Errorf("must be a function identifier (schema.name or name)")
	}
	return functionRefFromName(name)
}

// functionRefFromName compiles a bare (possibly dotted) function-reference
// identifier into query.ts's FunctionIdentifier JSON form : dotted ->
// {schema, name}, bare -> the plain name string.
func functionRefFromName(name string) (any, error) {
	if idx := strings.LastIndexByte(name, '.'); idx >= 0 {
		return map[string]any{"schema": name[:idx], "name": name[idx+1:]}, nil
	}
	return name, nil
}

// parseFullExpr parses s as exactly one expr, erroring if any trailing,
// non-whitespace input remains — used for `where` and any other query key
// whose whole value is one expression (not a comma-list).
func parseFullExpr(s string) (any, error) {
	p := newExprParser(s)
	v, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if !p.atEnd() {
		return nil, oops.Errorf("unexpected trailing input at position %d in %q", p.i, s)
	}
	return v, nil
}

// parseTopLevelExprList parses s as a comma-separated list of `expr`
// entries (specs/query-json.md's "## Comma lists share the expression
// grammar's own tokenizer"), reusing the exact same parseExpr/comma loop
// parseArgList uses for a call's own argument list — the top level of a
// comma-list is syntactically identical to being "inside a call's
// parentheses" minus the parentheses themselves.
func parseTopLevelExprList(s string) ([]any, error) {
	p := newExprParser(s)
	if p.atEnd() {
		return nil, nil
	}
	var out []any
	for {
		v, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.skipSpace()
		if p.i >= len(p.s) {
			return out, nil
		}
		if p.s[p.i] != ',' {
			return nil, oops.Errorf("expected ',' or end of input at position %d in %q", p.i, s)
		}
		p.i++
	}
}
