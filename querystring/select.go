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

// select.go compiles specs/query-json.md's `select=` value : either one
// own/full-family call taking up the entire value (## own / full), or a
// plain comma-list of [alias:]expr entries compiling to the OBJECT variant
// of Expression (## select). The two are mutually exclusive per the spec,
// tried in that order.
package querystring

import (
	"strings"

	"github.com/samber/oops"
)

var ownFullCallNames = map[string]bool{
	"own_except": true, "full_except": true,
	"own_and": true, "full_and": true,
	"own_except_and": true, "full_except_and": true,
}

// compileSelect implements ## select's two mutually-exclusive forms.
func compileSelect(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, oops.Errorf("select must not be empty")
	}
	if v, ok, err := tryCompileOwnFull(s); err != nil {
		return nil, err
	} else if ok {
		return v, nil
	}
	return compileSelectCommaList(s)
}

// tryCompileOwnFull matches only when own/full (or family) takes up the
// entire value ; ok is false, no error, otherwise — falls through to the comma-list.
func tryCompileOwnFull(s string) (any, bool, error) {
	p := newExprParser(s)
	p.skipSpace()
	if p.i >= len(p.s) || !isIdentStart(p.s[p.i]) {
		return nil, false, nil
	}
	ident, err := p.parseIdentifier()
	if err != nil {
		return nil, false, nil
	}
	p.skipSpace()

	switch ident {
	case "own", "full":
		if !p.atEnd() {
			return nil, false, nil
		}
		return []any{ident}, true, nil
	}

	if !ownFullCallNames[ident] {
		return nil, false, nil
	}
	if p.i >= len(p.s) || p.s[p.i] != '(' {
		return nil, false, nil
	}
	p.i++ // consume '('

	switch ident {
	case "own_except", "full_except":
		except, err := parseIdentListUntilClose(p)
		if err != nil {
			return nil, true, err
		}
		if err := requireExhausted(p); err != nil {
			return nil, true, err
		}
		return []any{ident, except}, true, nil

	case "own_and", "full_and":
		and, err := parseAndMapUntilClose(p)
		if err != nil {
			return nil, true, err
		}
		if err := requireExhausted(p); err != nil {
			return nil, true, err
		}
		return []any{ident, and}, true, nil

	case "own_except_and", "full_except_and":
		except, and, err := parseExceptAndUntilClose(p)
		if err != nil {
			return nil, true, err
		}
		if err := requireExhausted(p); err != nil {
			return nil, true, err
		}
		return []any{ident, except, and}, true, nil
	}
	return nil, false, nil
}

func requireExhausted(p *exprParser) error {
	if !p.atEnd() {
		return oops.Errorf("unexpected trailing input at position %d in %q", p.i, p.s)
	}
	return nil
}

// parseIdentListUntilClose parses own_except/full_except's argument list :
// a comma-list of bare identifiers through the closing ')'.
func parseIdentListUntilClose(p *exprParser) ([]string, error) {
	var out []string
	p.skipSpace()
	if p.i < len(p.s) && p.s[p.i] == ')' {
		p.i++
		return out, nil
	}
	for {
		p.skipSpace()
		ident, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		out = append(out, ident)
		p.skipSpace()
		if p.i >= len(p.s) {
			return nil, oops.Errorf("unterminated except-list, expected ',' or ')'")
		}
		switch p.s[p.i] {
		case ',':
			p.i++
		case ')':
			p.i++
			return out, nil
		default:
			return nil, oops.Errorf("expected ',' or ')' at position %d in %q", p.i, p.s)
		}
	}
}

// parseAndMapUntilClose parses own_and/full_and's argument list : a
// comma-list of [alias:]expr entries through the closing ')'.
func parseAndMapUntilClose(p *exprParser) (map[string]any, error) {
	out := map[string]any{}
	p.skipSpace()
	if p.i < len(p.s) && p.s[p.i] == ')' {
		p.i++
		return out, nil
	}
	for {
		alias, value, err := parseSelectEntry(p)
		if err != nil {
			return nil, err
		}
		out[alias] = value
		p.skipSpace()
		if p.i >= len(p.s) {
			return nil, oops.Errorf("unterminated and-map, expected ',' or ')'")
		}
		switch p.s[p.i] {
		case ',':
			p.i++
		case ')':
			p.i++
			return out, nil
		default:
			return nil, oops.Errorf("expected ',' or ')' at position %d in %q", p.i, p.s)
		}
	}
}

// parseExceptAndUntilClose parses an except-list, a literal ';' (special
// to this form alone), then an and-map, through the closing ')'.
func parseExceptAndUntilClose(p *exprParser) ([]string, map[string]any, error) {
	var except []string
	p.skipSpace()
	for {
		p.skipSpace()
		if p.i < len(p.s) && p.s[p.i] == ';' {
			break
		}
		ident, err := p.parseIdentifier()
		if err != nil {
			return nil, nil, err
		}
		except = append(except, ident)
		p.skipSpace()
		if p.i >= len(p.s) {
			return nil, nil, oops.Errorf("unterminated except_and, expected ',' or ';'")
		}
		switch p.s[p.i] {
		case ',':
			p.i++
			continue
		case ';':
			goto and_map
		default:
			return nil, nil, oops.Errorf("expected ',' or ';' at position %d in %q", p.i, p.s)
		}
	}
and_map:
	if p.i >= len(p.s) || p.s[p.i] != ';' {
		return nil, nil, oops.Errorf("expected ';' separating the except-list from the and-map")
	}
	p.i++ // consume ';'
	and, err := parseAndMapUntilClose(p)
	if err != nil {
		return nil, nil, err
	}
	return except, and, nil
}

// parseSelectEntry parses one [alias:]expr entry : ident immediately
// followed by ':' is an alias, otherwise the whole entry is one expr.
func parseSelectEntry(p *exprParser) (alias string, value any, err error) {
	p.skipSpace()
	if p.i < len(p.s) && isIdentStart(p.s[p.i]) {
		saved := p.i
		ident, ierr := p.parseIdentifier()
		if ierr == nil {
			if p.i < len(p.s) && p.s[p.i] == ':' {
				p.i++
				v, verr := p.parseExpr()
				if verr != nil {
					return "", nil, verr
				}
				return ident, v, nil
			}
		}
		p.i = saved
	}
	v, verr := p.parseExpr()
	if verr != nil {
		return "", nil, verr
	}
	ident, ok := v.(string)
	if !ok {
		return "", nil, oops.Errorf("a select entry without an alias must be a plain identifier")
	}
	return ident, v, nil
}

// compileSelectCommaList implements ## select's plain-comma-list form.
func compileSelectCommaList(s string) (any, error) {
	p := newExprParser(s)
	out := map[string]any{}
	for {
		alias, value, err := parseSelectEntry(p)
		if err != nil {
			return nil, err
		}
		out[alias] = value
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

// compileOrderBy implements ## order_by ; a leading "-" immediately
// followed by a digit opens a negative number, not a descending marker.
func compileOrderBy(s string) ([]any, error) {
	p := newExprParser(s)
	if p.atEnd() {
		return nil, nil
	}
	var out []any
	for {
		p.skipSpace()
		desc := false
		if p.i < len(p.s) && p.s[p.i] == '-' && p.i+1 < len(p.s) && isIdentStart(p.s[p.i+1]) {
			desc = true
			p.i++
		}
		v, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if desc {
			out = append(out, []any{"desc", v})
		} else {
			out = append(out, v)
		}
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
