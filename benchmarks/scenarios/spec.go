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

// Package scenarios defines the benchmark scenarios : for each operation, the
// request every server needs, and how to turn its answer into a form that can
// be compared across servers. A single Field list drives all four request
// shapes (rel JSON, PostgREST query string, GraphQL selection) and all four
// normalizers, so the servers are always asked for the same data.
package scenarios

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/bytedance/sonic"
	"github.com/rel-server/rel/benchmarks/harness"
	"github.com/samber/oops"
)

type kind int

const (
	Int kind = iota
	Num
	Str
	Bool
	// Enum values are compared lower-cased : GraphQL spells them in upper case.
	Enum
	// One is an outgoing embed (a single object or null).
	One
	// Many is an incoming embed (a list ; compared sorted by "id" when it has one).
	Many
	// Count is the number of rows of an incoming embed.
	Count
	// Composite is one field of a composite-typed column.
	Composite
)

// Field is one output value. Name is the canonical (snake case) name.
type Field struct {
	Name string
	Kind kind

	// Embeds (One, Many, Count): the related table, how it joins (child
	// column -> parent column), and the foreign-key column that tells
	// PostgREST which relationship to follow ("" when unambiguous).
	Relation string
	On       map[string]string
	RestFK   string
	// Gql is the GraphQL field name of the embed when it is not camel(Name).
	Gql string
	Sub []Field

	// Composite : the composite column and the key read from it.
	Col, Key string
}

// dialect says which PostGraphile naming a GraphQL request follows.
type dialect struct{ v5 bool }

func (d dialect) server() harness.Server {
	if d.v5 {
		return harness.PostGraphile5
	}
	return harness.PostGraphile4
}

func camel(s string) string {
	var b strings.Builder
	up := false
	for _, r := range s {
		if r == '_' {
			up = true
			continue
		}
		if up {
			r = unicode.ToUpper(r)
			up = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (f Field) gqlName(d dialect) string {
	switch {
	case f.Gql != "":
		return f.Gql
	case f.Name == "id" && d.v5:
		return "rowId" // v5 keeps `id` for the global node id
	}
	return camel(f.Name)
}

// ---- request fragments ----------------------------------------------------

func relSelect(fields []Field) (sel map[string]any, join map[string]any) {
	sel, join = map[string]any{}, map[string]any{}
	for _, f := range fields {
		switch f.Kind {
		case One, Many:
			s, j := relSelect(f.Sub)
			jn := map[string]any{"relation": f.Relation, "schema": "hotel", "on": f.On, "select": s}
			if len(j) > 0 {
				jn["join"] = j
			}
			join[f.Name] = jn
			sel[f.Name] = []any{".", f.Name}
		case Count:
			join[f.Name] = map[string]any{"relation": f.Relation, "schema": "hotel", "on": f.On}
			sel[f.Name] = []any{"agg", "count", []any{[]any{".", f.Name, "id"}}}
		case Composite:
			sel[f.Name] = []any{".", []any{"col", f.Col}, f.Key}
		default:
			sel[f.Name] = []any{"col", f.Name}
		}
	}
	return sel, join
}

func restSelect(fields []Field) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		switch f.Kind {
		case One, Many:
			hint := ""
			if f.RestFK != "" {
				hint = "!" + f.RestFK
			}
			parts[i] = fmt.Sprintf("%s:%s%s(%s)", f.Name, f.Relation, hint, restSelect(f.Sub))
		case Count:
			parts[i] = fmt.Sprintf("%s:%s(count)", f.Name, f.Relation)
		case Composite:
			parts[i] = fmt.Sprintf("%s:%s->>%s", f.Name, f.Col, f.Key)
		default:
			parts[i] = f.Name
		}
	}
	return strings.Join(parts, ",")
}

func gqlSelect(fields []Field, d dialect) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		switch f.Kind {
		case One:
			parts[i] = fmt.Sprintf("%s: %s { %s }", f.Name, f.gqlName(d), gqlSelect(f.Sub, d))
		case Many:
			parts[i] = fmt.Sprintf("%s: %s { nodes { %s } }", f.Name, f.gqlName(d), gqlSelect(f.Sub, d))
		case Count:
			parts[i] = fmt.Sprintf("%s: %s { totalCount }", f.Name, f.gqlName(d))
		case Composite:
			parts[i] = fmt.Sprintf("%s: %s { %s }", f.Name, camel(f.Col), f.Key)
		default:
			parts[i] = fmt.Sprintf("%s: %s", f.Name, f.gqlName(d))
		}
	}
	return strings.Join(parts, " ")
}

// ---- normalization ----------------------------------------------------------
//
// GraphQL selections are aliased to the canonical names (see gqlSelect), so
// every source yields objects keyed by canonical names ; only embed wrappers
// ({nodes}, {totalCount}, [{count}]) differ.

type source int

const (
	srcRel source = iota
	srcRest
	srcGql
)

func canonRow(fields []Field, row map[string]any, src source) (map[string]any, error) {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		v := row[f.Name]
		switch f.Kind {
		case Int, Num:
			n, err := toNumber(v)
			if err != nil {
				return nil, oops.With("field", f.Name).Wrapf(err, "reading number")
			}
			out[f.Name] = n
		case Str:
			out[f.Name] = v
		case Composite:
			if m, ok := v.(map[string]any); ok && src == srcGql {
				v = m[f.Key] // aliased selection : {city: {city: ...}}
			}
			out[f.Name] = v
		case Bool:
			out[f.Name] = v
		case Enum:
			s, _ := v.(string)
			out[f.Name] = strings.ToLower(s)
		case One:
			if v == nil {
				out[f.Name] = nil
				continue
			}
			m, ok := v.(map[string]any)
			if !ok {
				return nil, oops.With("field", f.Name).Errorf("expected object, got %T", v)
			}
			sub, err := canonRow(f.Sub, m, src)
			if err != nil {
				return nil, err
			}
			out[f.Name] = sub
		case Many:
			list, err := unwrapList(v, src)
			if err != nil {
				return nil, oops.With("field", f.Name).Wrap(err)
			}
			rows, err := canonRows(f.Sub, list, src)
			if err != nil {
				return nil, err
			}
			out[f.Name] = rows
		case Count:
			n, err := countOf(v, src)
			if err != nil {
				return nil, oops.With("field", f.Name).Wrap(err)
			}
			out[f.Name] = n
		}
	}
	return out, nil
}

func unwrapList(v any, src source) ([]any, error) {
	if src == srcGql {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, oops.Errorf("expected connection object, got %T", v)
		}
		v = m["nodes"]
	}
	list, ok := v.([]any)
	if !ok {
		return nil, oops.Errorf("expected list, got %T", v)
	}
	return list, nil
}

func countOf(v any, src source) (float64, error) {
	switch src {
	case srcGql:
		if m, ok := v.(map[string]any); ok {
			return toNumber(m["totalCount"])
		}
	case srcRest:
		if l, ok := v.([]any); ok && len(l) == 1 {
			if m, ok := l[0].(map[string]any); ok {
				return toNumber(m["count"])
			}
		}
	default:
		return toNumber(v)
	}
	return 0, oops.Errorf("unexpected count shape %T", v)
}

func toNumber(v any) (float64, error) {
	switch t := v.(type) {
	case float64:
		return t, nil
	case string:
		return strconv.ParseFloat(t, 64)
	case nil:
		return 0, oops.Errorf("null where a number was expected")
	}
	return 0, oops.Errorf("not a number: %T", v)
}

func canonRows(fields []Field, list []any, src source) ([]any, error) {
	rows := make([]any, len(list))
	for i, it := range list {
		m, ok := it.(map[string]any)
		if !ok {
			return nil, oops.Errorf("row %d is %T, not an object", i, it)
		}
		r, err := canonRow(fields, m, src)
		if err != nil {
			return nil, err
		}
		rows[i] = r
	}
	return rows, nil
}

// sortEmbeds orders the rows of every Many embed by id, since servers do not
// promise an order for nested lists.
func sortEmbeds(fields []Field, rows []any) {
	for _, f := range fields {
		if f.Kind != Many && f.Kind != One {
			continue
		}
		for _, r := range rows {
			m := r.(map[string]any)
			switch sub := m[f.Name].(type) {
			case []any:
				sortEmbeds(f.Sub, sub)
				if hasField(f.Sub, "id") {
					sort.SliceStable(sub, func(i, j int) bool {
						return sub[i].(map[string]any)["id"].(float64) < sub[j].(map[string]any)["id"].(float64)
					})
				}
			case map[string]any:
				sortEmbeds(f.Sub, []any{sub})
			}
		}
	}
}

func hasField(fields []Field, name string) bool {
	for _, f := range fields {
		if f.Name == name && (f.Kind == Int) {
			return true
		}
	}
	return false
}

// normalizer builds the Impl.Normalize for a read whose answer is a list of
// rows (rel, PostgREST) or a GraphQL connection / single object at data.<root>.
func normalizer(fields []Field, src source, root string, single bool) func([]byte) (any, error) {
	return func(body []byte) (any, error) {
		var doc any
		if err := sonic.Unmarshal(body, &doc); err != nil {
			return nil, oops.Wrapf(err, "decoding response")
		}
		var list []any
		switch src {
		case srcGql:
			m, _ := doc.(map[string]any)
			if errs, bad := m["errors"]; bad {
				return nil, oops.Errorf("graphql errors: %v", errs)
			}
			data, _ := m["data"].(map[string]any)
			node := data[root]
			if single {
				if node == nil {
					return []any{}, nil
				}
				list = []any{node}
			} else {
				l, err := unwrapList(node, srcGql)
				if err != nil {
					return nil, err
				}
				list = l
			}
		default:
			l, ok := doc.([]any)
			if !ok {
				return nil, oops.Errorf("expected a JSON array, got %T", doc)
			}
			list = l
		}
		rows, err := canonRows(fields, list, src)
		if err != nil {
			return nil, err
		}
		sortEmbeds(fields, rows)
		return rows, nil
	}
}
