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

// Filter expression grammar, producing the same plain values as a JSON Expression. Used for `where`, every
// select/order_by/distinct_on comma-list, and call arguments.
package querystring

import (
	"strconv"
	"strings"

	"github.com/rel-server/rel/query"
	"github.com/samber/oops"
)

// exprParser is a left-to-right scanner over one query-string value.
type exprParser struct {
	s string
	i int
}

func newExprParser(s string) *exprParser { return &exprParser{s: s} }

// querystringOnlyKeywords are call identifiers with no query.OperatorWords
// entry that still need special dispatch, not a plain function call.
var querystringOnlyKeywords = map[string]string{
	"bigint":  "bigint",
	"numeric": "numeric",
}

// parseTopValue parses a value position (where, a comma-list entry), where a bare identifier becomes its
// [".", name] lookup and a quoted 'string' is a literal. Call arguments use parseExpr, since agg/call need the raw
// function name.
func (p *exprParser) parseTopValue() (any, error) {
	p.skipSpace()
	if p.i < len(p.s) && p.s[p.i] == '\'' {
		return p.parseStringLiteral()
	}
	v, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if s, ok := v.(string); ok {
		return []any{".", s}, nil
	}
	return v, nil
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

// parseIdentifier consumes a possibly dotted identifier, e.g. "actors.name".
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

// parseStringLiteral consumes a 'quoted string', a doubled quote standing for one, and returns its content.
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

// parseNumber consumes -?[0-9]+(\.[0-9]+)? as a float64.
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

// parseExpr parses one call or atom ; a bare identifier comes back as a plain string, undecided.
func (p *exprParser) parseExpr() (any, error) {
	p.skipSpace()
	if p.i >= len(p.s) {
		return nil, oops.Errorf("unexpected end of expression in %q", p.s)
	}
	c := p.s[p.i]
	switch {
	case c == '\'':
		// Unreachable : parseTopValue and parseArgList handle a quote first.
		lit, err := p.parseStringLiteral()
		if err != nil {
			return nil, err
		}
		return lit, nil

	case c == '-' || isDigit(c):
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

// parseCall compiles ident(args...) to its array form, the cursor at "(".
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
		// An unknown name is a plain function call.
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
	case "any", "all":
		if len(args) != 3 {
			return nil, oops.Errorf("%q needs exactly 3 operands (operator, subject, array), got %d", ident, len(args))
		}
		opArg, ok := args[0].(identArg)
		if !ok {
			return nil, oops.Errorf("%q's operator must be a bare operator word, not a quoted string", ident)
		}
		opStr := string(opArg)
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
			out = append(out, a.value())
		}
		return out, nil

	case "format":
		// The format string is a raw string, not an Expression.
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
		// "-"/"~" each cover two operators of different arity ; the word
		// chosen must be checked against the arity it claims.
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

// literalString marks an argument written as a quoted literal.
type literalString string

// callArg is one parsed call argument, keeping how it was written.
type callArg interface{ value() any }

func (s literalString) value() any { return string(s) }

// identArg marks a bare identifier argument : value() is its [".", name] lookup, but agg/call's function argument
// reads the raw name.
type identArg string

func (a identArg) value() any { return []any{".", string(a)} }

// plainArg wraps any other compiled value : a call result, number, bool or null.
type plainArg struct{ v any }

func (a plainArg) value() any { return a.v }

// parseArgList consumes [expr ("," expr)*] ")", the cursor past '('.
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
			if s, ok := v.(string); ok {
				arg = identArg(s)
			} else {
				arg = plainArg{v}
			}
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

// functionRefFromArg compiles agg/call's first argument into a function identifier ; it must be unquoted.
func functionRefFromArg(a callArg) (any, error) {
	ia, ok := a.(identArg)
	if !ok {
		return nil, oops.Errorf("must be a function identifier (schema.name or name), not a quoted string")
	}
	return functionRefFromName(string(ia))
}

// functionRefFromName compiles an identifier into a function identifier : dotted -> {schema, name}, bare -> itself.
func functionRefFromName(name string) (any, error) {
	if idx := strings.LastIndexByte(name, '.'); idx >= 0 {
		return map[string]any{"schema": name[:idx], "name": name[idx+1:]}, nil
	}
	return name, nil
}

// parseFullExpr parses s as exactly one expr, erroring on trailing input.
func parseFullExpr(s string) (any, error) {
	p := newExprParser(s)
	v, err := p.parseTopValue()
	if err != nil {
		return nil, err
	}
	if !p.atEnd() {
		return nil, oops.Errorf("unexpected trailing input at position %d in %q", p.i, s)
	}
	return v, nil
}

// parseTopLevelExprList parses a comma-separated list of value entries, without parentheses.
func parseTopLevelExprList(s string) ([]any, error) {
	p := newExprParser(s)
	if p.atEnd() {
		return nil, nil
	}
	var out []any
	for {
		v, err := p.parseTopValue()
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
