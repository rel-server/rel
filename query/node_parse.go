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

// Pass 1, decode step : query.ts's JSON shape decoded into the raw* types
// below, hand-written over sonic/ast (no struct-tag unmarshal — see
// querying.md ## Implementation details), mirroring expression_parse.go's
// style and reusing its unexported helpers directly (same package). Nothing
// here touches pg or config : that's node_resolve.go's job. Expression-typed
// fields ARE resolved to query.Expression at this stage (via parseNode),
// since that's pure parsing, independent of DB/scope resolution.
package query

import (
	"bytes"
	"fmt"

	"github.com/bytedance/sonic/ast"
)

// ParsedQuery is the decoded (not yet DB-resolved) form of query.ts's
// top-level Query = WriteQuery | Relation | WellKnownQuery | Query[] union.
// Exactly one field is set, distinguished structurally (no explicit tag
// field exists in the JSON to dispatch on) :
//   - a JSON array -> Sequence
//   - an object with a "wellknown" key -> WellKnown
//   - an object with a "query" key -> Write
//   - anything else (a bare Relation object) -> Relation
type ParsedQuery struct {
	Relation  *rawRelation
	Write     *rawWriteQuery
	WellKnown *rawWellKnown
	Sequence  []ParsedQuery
}

// rawWriteQuery is query.ts's WriteQuery. Data is query.ts's `data: any` —
// not interpreted here ; kept as raw JSON for whichever later stage
// (writing algorithm) actually walks it against the resolved tree.
type rawWriteQuery struct {
	Query *rawRelation
	Data  []byte
}

// rawWellKnown is query.ts's WellKnownQuery. Params/Data are `any` — same
// "not this stage's job" reasoning as rawWriteQuery.Data.
type rawWellKnown struct {
	WellKnown string
	Params    []byte
	Data      []byte
}

// ParseQuery decodes one top-level query.ts Query value.
func ParseQuery(data []byte) (ParsedQuery, error) {
	trimmed := bytes.TrimSpace(data)
	root, perr := ast.NewParser(string(trimmed)).Parse()
	if perr != 0 {
		return ParsedQuery{}, fmt.Errorf("query: invalid query JSON: %w", perr)
	}
	return parseQueryNode(&root)
}

func parseQueryNode(n *ast.Node) (ParsedQuery, error) {
	switch n.TypeSafe() {
	case ast.V_ARRAY:
		items, err := n.ArrayUseNode()
		if err != nil {
			return ParsedQuery{}, fmt.Errorf("query: invalid query sequence: %w", err)
		}
		seq := make([]ParsedQuery, len(items))
		for i := range items {
			pq, err := parseQueryNode(&items[i])
			if err != nil {
				return ParsedQuery{}, fmt.Errorf("query: sequence item %d: %w", i, err)
			}
			seq[i] = pq
		}
		return ParsedQuery{Sequence: seq}, nil

	case ast.V_OBJECT:
		if wk := n.Get("wellknown"); wk.Exists() {
			name, err := wk.StrictString()
			if err != nil {
				return ParsedQuery{}, fmt.Errorf(`query: "wellknown" must be a string: %w`, err)
			}
			rawWK := &rawWellKnown{WellKnown: name}
			if p := n.Get("params"); p.Exists() {
				if raw, err := p.Raw(); err == nil {
					rawWK.Params = []byte(raw)
				}
			}
			if d := n.Get("data"); d.Exists() {
				if raw, err := d.Raw(); err == nil {
					rawWK.Data = []byte(raw)
				}
			}
			return ParsedQuery{WellKnown: rawWK}, nil
		}

		if q := n.Get("query"); q.Exists() {
			rel, err := parseRawRelation(q)
			if err != nil {
				return ParsedQuery{}, fmt.Errorf(`query: "query": %w`, err)
			}
			rawWQ := &rawWriteQuery{Query: rel}
			d := n.Get("data")
			if !d.Exists() {
				return ParsedQuery{}, fmt.Errorf(`query: a WriteQuery needs "data"`)
			}
			raw, err := d.Raw()
			if err != nil {
				return ParsedQuery{}, fmt.Errorf(`query: "data": %w`, err)
			}
			rawWQ.Data = []byte(raw)
			return ParsedQuery{Write: rawWQ}, nil
		}

		rel, err := parseRawRelation(n)
		if err != nil {
			return ParsedQuery{}, err
		}
		return ParsedQuery{Relation: rel}, nil

	default:
		return ParsedQuery{}, fmt.Errorf("query: top-level query must be an object or an array, got type %d", n.TypeSafe())
	}
}

// rawRelation is query.ts's Relation, decoded one field at a time but not
// yet resolved against pg — Relation/Function/Schema stay plain strings, On
// stays a plain map, Join recurses. See node_resolve.go for what turns this
// into a *QueryNode.
type rawRelation struct {
	// Exactly one of Relation/Function is non-empty (IsFunction says which)
	// — query.ts's "relation" and "function" keys are mutually exclusive, so
	// a node names either a table/view or a function call, never both and
	// never neither ; parseRawRelation rejects an empty string for whichever
	// key was given, so this holds as a real invariant, not just "whichever
	// key was present, however it decoded."
	Relation   string
	Function   string
	IsFunction bool

	Schema string // "" : unqualified, resolved via search path
	Alias  string

	On map[string]string // nil if absent

	// Only meaningful when IsFunction. Absent entirely (both nil) for a
	// function call taking no arguments — unlike the old "relation"+
	// "arguments" scheme, there's no ambiguity to guard against here, since
	// "function" alone already says this node is a call.
	ArgumentsPositional []Expression
	ArgumentsNamed      map[string]Expression

	Where Expression // nil if absent

	WriteMode string // raw query.ts string, "" if absent

	OnConflictConstraintName string // one or the other, per query.ts's on_conflict : string | string[]
	OnConflictColumns        []string

	InsertColumns []string
	UpdateColumns []string

	Join map[string]*rawRelation // nil if absent ; key is the join alias (OuterAlias)

	Select Expression // nil if absent -> resolve step defaults to FullExpr{}

	Distinct   bool
	DistinctOn []Expression

	OrderBy []OrderByTerm

	Offset *int
	Limit  *int
}

var orderByDirectionTags = map[string]OrderByDirection{
	"asc":             OrderAsc,
	"desc":            OrderDesc,
	"asc-nulls-first": OrderAscNullsFirst,
	"desc-nulls-last": OrderDescNullsLast,
}

func parseRawRelation(n *ast.Node) (*rawRelation, error) {
	relNode := n.Get("relation")
	fnNode := n.Get("function")
	if relNode.Exists() == fnNode.Exists() {
		if relNode.Exists() {
			return nil, fmt.Errorf(`query: relation object must have exactly one of "relation" or "function", not both`)
		}
		return nil, fmt.Errorf(`query: relation object needs exactly one of "relation" or "function"`)
	}

	raw := &rawRelation{}
	var err error
	if relNode.Exists() {
		raw.Relation, err = relNode.StrictString()
		if err != nil {
			return nil, fmt.Errorf(`query: "relation" must be a string: %w`, err)
		}
		if raw.Relation == "" {
			return nil, fmt.Errorf(`query: "relation" must not be empty`)
		}
	} else {
		raw.IsFunction = true
		raw.Function, err = fnNode.StrictString()
		if err != nil {
			return nil, fmt.Errorf(`query: "function" must be a string: %w`, err)
		}
		if raw.Function == "" {
			return nil, fmt.Errorf(`query: "function" must not be empty`)
		}
	}

	if s := n.Get("schema"); s.Exists() {
		raw.Schema, err = s.StrictString()
		if err != nil {
			return nil, fmt.Errorf(`query: "schema" must be a string: %w`, err)
		}
	}
	if a := n.Get("alias"); a.Exists() {
		raw.Alias, err = a.StrictString()
		if err != nil {
			return nil, fmt.Errorf(`query: "alias" must be a string: %w`, err)
		}
	}

	if on := n.Get("on"); on.Exists() {
		raw.On, err = parseStringMap(on)
		if err != nil {
			return nil, fmt.Errorf(`query: "on": %w`, err)
		}
	}

	if args := n.Get("arguments"); args.Exists() {
		if !raw.IsFunction {
			return nil, fmt.Errorf(`query: "arguments" is only valid alongside "function", not "relation"`)
		}
		switch args.TypeSafe() {
		case ast.V_ARRAY:
			items, err := args.ArrayUseNode()
			if err != nil {
				return nil, fmt.Errorf(`query: "arguments": %w`, err)
			}
			raw.ArgumentsPositional, err = parseExpressionList(items)
			if err != nil {
				return nil, fmt.Errorf(`query: "arguments": %w`, err)
			}
		case ast.V_OBJECT:
			raw.ArgumentsNamed, err = parseObjectFields(args)
			if err != nil {
				return nil, fmt.Errorf(`query: "arguments": %w`, err)
			}
		default:
			return nil, fmt.Errorf(`query: "arguments" must be an array or an object`)
		}
	}

	if w := n.Get("where"); w.Exists() {
		raw.Where, err = parseNode(w)
		if err != nil {
			return nil, fmt.Errorf(`query: "where": %w`, err)
		}
	}

	if wm := n.Get("write_mode"); wm.Exists() {
		raw.WriteMode, err = wm.StrictString()
		if err != nil {
			return nil, fmt.Errorf(`query: "write_mode" must be a string: %w`, err)
		}
	}

	if oc := n.Get("on_conflict"); oc.Exists() {
		switch oc.TypeSafe() {
		case ast.V_STRING:
			raw.OnConflictConstraintName, err = oc.StrictString()
			if err != nil {
				return nil, fmt.Errorf(`query: "on_conflict": %w`, err)
			}
		case ast.V_ARRAY:
			raw.OnConflictColumns, err = parseStringListValue(oc)
			if err != nil {
				return nil, fmt.Errorf(`query: "on_conflict": %w`, err)
			}
		default:
			return nil, fmt.Errorf(`query: "on_conflict" must be a string or a string array`)
		}
	}

	if ic := n.Get("insert_columns"); ic.Exists() {
		raw.InsertColumns, err = parseStringListValue(ic)
		if err != nil {
			return nil, fmt.Errorf(`query: "insert_columns": %w`, err)
		}
	}
	if uc := n.Get("update_columns"); uc.Exists() {
		raw.UpdateColumns, err = parseStringListValue(uc)
		if err != nil {
			return nil, fmt.Errorf(`query: "update_columns": %w`, err)
		}
	}

	if j := n.Get("join"); j.Exists() {
		fields, err := j.MapUseNode()
		if err != nil {
			return nil, fmt.Errorf(`query: "join" must be an object: %w`, err)
		}
		raw.Join = make(map[string]*rawRelation, len(fields))
		for alias, child := range fields {
			childRaw, err := parseRawRelation(&child)
			if err != nil {
				return nil, fmt.Errorf(`query: join %q: %w`, alias, err)
			}
			raw.Join[alias] = childRaw
		}
	}

	if s := n.Get("select"); s.Exists() {
		raw.Select, err = parseNode(s)
		if err != nil {
			return nil, fmt.Errorf(`query: "select": %w`, err)
		}
	}

	if d := n.Get("distinct"); d.Exists() {
		raw.Distinct, err = d.StrictBool()
		if err != nil {
			return nil, fmt.Errorf(`query: "distinct" must be a boolean: %w`, err)
		}
	}
	if do := n.Get("distinct_on"); do.Exists() {
		items, err := do.ArrayUseNode()
		if err != nil {
			return nil, fmt.Errorf(`query: "distinct_on" must be an array: %w`, err)
		}
		raw.DistinctOn, err = parseExpressionList(items)
		if err != nil {
			return nil, fmt.Errorf(`query: "distinct_on": %w`, err)
		}
	}

	if ob := n.Get("order_by"); ob.Exists() {
		items, err := ob.ArrayUseNode()
		if err != nil {
			return nil, fmt.Errorf(`query: "order_by" must be an array: %w`, err)
		}
		raw.OrderBy = make([]OrderByTerm, len(items))
		for i := range items {
			term, err := parseOrderByTerm(&items[i])
			if err != nil {
				return nil, fmt.Errorf(`query: "order_by" item %d: %w`, i, err)
			}
			raw.OrderBy[i] = term
		}
	}

	if o := n.Get("offset"); o.Exists() {
		v, err := o.StrictInt64()
		if err != nil {
			return nil, fmt.Errorf(`query: "offset" must be a number: %w`, err)
		}
		offset := int(v)
		raw.Offset = &offset
	}
	if l := n.Get("limit"); l.Exists() {
		v, err := l.StrictInt64()
		if err != nil {
			return nil, fmt.Errorf(`query: "limit" must be a number: %w`, err)
		}
		limit := int(v)
		raw.Limit = &limit
	}

	return raw, nil
}

// parseOrderByTerm disambiguates order_by's per-item union : a bare
// Expression (default OrderAsc), or an explicit [direction, Expression]
// pair. Only exactly one of the four known direction strings in the first
// slot of a 2-element array counts as the tuple form — anything else falls
// through to being parsed as a bare (2-element-array-shaped) Expression, no
// different from how expression_parse.go's own tag dispatch avoids
// collisions elsewhere.
func parseOrderByTerm(n *ast.Node) (OrderByTerm, error) {
	if n.TypeSafe() == ast.V_ARRAY {
		if items, err := n.ArrayUseNode(); err == nil && len(items) == 2 {
			if tag, err := items[0].StrictString(); err == nil {
				if dir, ok := orderByDirectionTags[tag]; ok {
					expr, err := parseNode(&items[1])
					if err != nil {
						return OrderByTerm{}, err
					}
					return OrderByTerm{Expr: expr, Direction: dir}, nil
				}
			}
		}
	}
	expr, err := parseNode(n)
	if err != nil {
		return OrderByTerm{}, err
	}
	return OrderByTerm{Expr: expr, Direction: OrderAsc}, nil
}

func parseStringMap(n *ast.Node) (map[string]string, error) {
	fields, err := n.MapUseNode()
	if err != nil {
		return nil, fmt.Errorf("expected an object: %w", err)
	}
	out := make(map[string]string, len(fields))
	for k, v := range fields {
		s, err := v.StrictString()
		if err != nil {
			return nil, fmt.Errorf("field %q: expected a string: %w", k, err)
		}
		out[k] = s
	}
	return out, nil
}
