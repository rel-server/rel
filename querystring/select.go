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

// compileSelect implements ## select's two mutually-exclusive forms.
func compileSelect(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, oops.Errorf("select must not be empty")
	}
	if v, ok, err := tryCompileStar(s); err != nil {
		return nil, err
	} else if ok {
		return v, nil
	}
	return compileSelectCommaList(s)
}

// tryCompileStar matches only when "*"/"*~" (optionally called with an
// except-list and/or and-map) takes up the entire value ; ok is false, no
// error, otherwise — falls through to the comma-list. "*" isn't a valid
// identifier-start character, so this is handled directly rather than
// through parseIdentifier/parseCall.
func tryCompileStar(s string) (any, bool, error) {
	p := newExprParser(s)
	p.skipSpace()
	if p.i >= len(p.s) || p.s[p.i] != '*' {
		return nil, false, nil
	}
	p.i++
	tag := "*"
	if p.i < len(p.s) && p.s[p.i] == '~' {
		tag = "*~"
		p.i++
	}
	p.skipSpace()

	if p.atEnd() {
		return []any{tag}, true, nil
	}
	if p.i >= len(p.s) || p.s[p.i] != '(' {
		return nil, true, oops.Errorf("expected '(' or end of input at position %d in %q", p.i, s)
	}
	p.i++ // consume '('

	except, and, err := parseExceptAndUntilClose(p)
	if err != nil {
		return nil, true, err
	}
	if err := requireExhausted(p); err != nil {
		return nil, true, err
	}
	out := []any{tag}
	if except != nil {
		out = append(out, except)
	}
	if and != nil {
		out = append(out, and)
	}
	return out, true, nil
}

func requireExhausted(p *exprParser) error {
	if !p.atEnd() {
		return oops.Errorf("unexpected trailing input at position %d in %q", p.i, p.s)
	}
	return nil
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

// parseExceptAndUntilClose parses "*"/"*~"'s call-form argument list :
// an except-list, and/or (separated by a literal ';', special to this form
// alone) an and-map — each optional, through the closing ')'. Both nil
// means an empty call, "()". Unlike parseAndMapUntilClose, an absent and-map
// is nil (not an empty map), so the caller can tell "omitted" from "given
// but empty".
//
// An and-only call MUST still lead with ';' (e.g. "*~(;actors)") even
// though the except-list is empty — a bare identifier before ';' is
// otherwise genuinely ambiguous between "except this column" and "and this
// self-aliased column", and the old grammar's separate own_and/own_except
// tags never had to resolve that ambiguity.
func parseExceptAndUntilClose(p *exprParser) ([]string, map[string]any, error) {
	var except []string
	p.skipSpace()
	if p.i < len(p.s) && p.s[p.i] == ')' {
		p.i++
		return nil, nil, nil
	}
	if p.i < len(p.s) && p.s[p.i] == ';' {
		p.i++
		and, err := parseAndMapUntilClose(p)
		return nil, and, err
	}
	for {
		p.skipSpace()
		ident, err := p.parseIdentifier()
		if err != nil {
			return nil, nil, err
		}
		except = append(except, ident)
		p.skipSpace()
		if p.i >= len(p.s) {
			return nil, nil, oops.Errorf("unterminated except-list, expected ',', ';' or ')'")
		}
		switch p.s[p.i] {
		case ',':
			p.i++
			continue
		case ';':
			p.i++
			and, err := parseAndMapUntilClose(p)
			if err != nil {
				return nil, nil, err
			}
			return except, and, nil
		case ')':
			p.i++
			return except, nil, nil
		default:
			return nil, nil, oops.Errorf("expected ',', ';' or ')' at position %d in %q", p.i, p.s)
		}
	}
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
				v, verr := p.parseTopValue()
				if verr != nil {
					return "", nil, verr
				}
				return ident, v, nil
			}
		}
		p.i = saved
	}
	if p.i < len(p.s) && p.s[p.i] == '\'' {
		return "", nil, oops.Errorf("a select entry without an alias must be a plain identifier, not a quoted string")
	}
	v, verr := p.parseExpr()
	if verr != nil {
		return "", nil, verr
	}
	ident, ok := v.(string)
	if !ok {
		return "", nil, oops.Errorf("a select entry without an alias must be a plain identifier")
	}
	return ident, []any{".", ident}, nil
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

// compileOrderBy's leading "-" (specs/query-json.md ## Filter expression grammar) immediately
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
		v, err := p.parseTopValue()
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
