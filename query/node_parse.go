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
// query-engine.md ## Implementation), mirroring expression_parse.go's
// style and reusing its unexported helpers directly (same package). Nothing
// here touches pg or config : that's node_resolve.go's job. Expression-typed
// fields ARE resolved to query.Expression at this stage (via parseNode),
// since that's pure parsing, independent of DB/scope resolution.
package query

import (
	"bytes"

	"github.com/bytedance/sonic/ast"
	"github.com/rel-server/rel/errcode"
	"github.com/samber/oops"
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

// rawWriteQuery is query.ts's ComplexQuery ; exactly one of Query/WellKnown
// is set. Data stays raw JSON, walked later by the writing algorithm ; nil
// means a read (specs/complex-query.md ## Parsing), distinguished from an
// explicit JSON "data": null by Data being a non-nil 4-byte "null" slice.
type rawWriteQuery struct {
	Query     *rawRelation  // nil if WellKnown is set instead
	WellKnown *rawWellKnown // nil if Query is set instead
	Data      []byte

	// Returns/Count/Stats/QueryPlan/Sql/Rollback are ComplexQuery's own
	// response-shaping flags (specs/complex-query.md), decoded off the same
	// object as Query/WellKnown/Data. Returns is "" when absent (defaults
	// to "results" downstream), never validated to a known value here — see
	// ValidateReturns.
	Returns   string
	Count     bool
	Stats     bool
	QueryPlan bool
	Sql       bool
	Rollback  bool
}

// validReturnsValues is query.ts's ComplexQuery.returns union.
var validReturnsValues = map[string]bool{"": true, "none": true, "results": true}

// ValidateReturns rejects an unrecognized "returns" value — parseQueryNode
// only decodes the raw string, since query.ts's returns union isn't
// otherwise structurally distinguishable from a typo at decode time.
func (rq *rawWriteQuery) ValidateReturns() error {
	if !validReturnsValues[rq.Returns] {
		return oops.Code(errcode.QueryInvalidReturns).Errorf(`query: "returns" must be "none" or "results", got %q`, rq.Returns)
	}
	return nil
}

// rawWellKnown is query.ts's WellKnownQuery, no "data" of its own — written
// to only by wrapping it in WriteQuery.query, like a Relation.
type rawWellKnown struct {
	WellKnown string
	Params    []byte
}

// ParseQuery decodes one top-level query.ts Query value.
func ParseQuery(data []byte) (ParsedQuery, error) {
	trimmed := bytes.TrimSpace(data)
	root, perr := ast.NewParser(string(trimmed)).Parse()
	if perr != 0 {
		return ParsedQuery{}, oops.Wrapf(perr, "query: invalid query JSON")
	}
	return parseQueryNode(&root)
}

// boolField decodes n's key as a boolean, present reporting whether the key
// existed at all — the same "exists, then strictly typed" check every
// ComplexQuery response-shaping flag needs (rawWriteQuery's
// Count/Stats/QueryPlan/Sql/Rollback).
func boolField(n *ast.Node, key string) (val, present bool, err error) {
	x := n.Get(key)
	if !x.Exists() {
		return false, false, nil
	}
	b, err := x.StrictBool()
	if err != nil {
		return false, true, oops.With("field", key).Wrapf(err, "query: %q must be a boolean", key)
	}
	return b, true, nil
}

func parseQueryNode(n *ast.Node) (ParsedQuery, error) {
	switch n.TypeSafe() {
	case ast.V_ARRAY:
		items, err := n.ArrayUseNode()
		if err != nil {
			return ParsedQuery{}, oops.Wrapf(err, "query: invalid query sequence")
		}
		seq := make([]ParsedQuery, len(items))
		for i := range items {
			pq, err := parseQueryNode(&items[i])
			if err != nil {
				return ParsedQuery{}, oops.With("index", i).Wrapf(err, "query: sequence item %d", i)
			}
			seq[i] = pq
		}
		return ParsedQuery{Sequence: seq}, nil

	case ast.V_OBJECT:
		if wk := n.Get("wellknown"); wk.Exists() {
			rawWK, err := parseRawWellKnown(n, wk)
			if err != nil {
				return ParsedQuery{}, err
			}
			// Never its own "data" — wrap in {"query": {...}, "data": ...} instead.
			if n.Get("data").Exists() {
				return ParsedQuery{}, oops.Errorf(`query: a bare "wellknown" query never takes "data" directly — wrap it in {"query": {...}, "data": ...} instead`)
			}
			return ParsedQuery{WellKnown: rawWK}, nil
		}

		if q := n.Get("query"); q.Exists() {
			rawWQ := &rawWriteQuery{}
			if wk := q.Get("wellknown"); wk.Exists() {
				rawWK, err := parseRawWellKnown(q, wk)
				if err != nil {
					return ParsedQuery{}, oops.Wrapf(err, `query: "query"`)
				}
				rawWQ.WellKnown = rawWK
			} else {
				rel, err := parseRawRelation(q)
				if err != nil {
					return ParsedQuery{}, oops.Wrapf(err, `query: "query"`)
				}
				rawWQ.Query = rel
			}
			if d := n.Get("data"); d.Exists() {
				raw, err := d.Raw()
				if err != nil {
					return ParsedQuery{}, oops.Wrapf(err, `query: "data"`)
				}
				rawWQ.Data = []byte(raw)
			}
			if ret := n.Get("returns"); ret.Exists() {
				s, err := ret.StrictString()
				if err != nil {
					return ParsedQuery{}, oops.With("field", "returns").Wrapf(err, `query: "returns" must be a string`)
				}
				rawWQ.Returns = s
			}
			var err error
			if rawWQ.Count, _, err = boolField(n, "count"); err != nil {
				return ParsedQuery{}, err
			}
			if rawWQ.Stats, _, err = boolField(n, "stats"); err != nil {
				return ParsedQuery{}, err
			}
			if rawWQ.QueryPlan, _, err = boolField(n, "query_plan"); err != nil {
				return ParsedQuery{}, err
			}
			if rawWQ.Sql, _, err = boolField(n, "sql"); err != nil {
				return ParsedQuery{}, err
			}
			if rawWQ.Rollback, _, err = boolField(n, "rollback"); err != nil {
				return ParsedQuery{}, err
			}
			if err := rawWQ.ValidateReturns(); err != nil {
				return ParsedQuery{}, err
			}
			return ParsedQuery{Write: rawWQ}, nil
		}

		rel, err := parseRawRelation(n)
		if err != nil {
			return ParsedQuery{}, err
		}
		return ParsedQuery{Relation: rel}, nil

	default:
		return ParsedQuery{}, oops.With("json_type", int(n.TypeSafe())).Errorf("query: top-level query must be an object or an array, got type %d", n.TypeSafe())
	}
}

// parseRawWellKnown decodes {wellknown, params?} off n (wk already
// confirmed to exist) ; shared so both call sites can't drift on params.
func parseRawWellKnown(n *ast.Node, wk *ast.Node) (*rawWellKnown, error) {
	name, err := wk.StrictString()
	if err != nil {
		return nil, oops.With("field", "wellknown").Wrapf(err, `query: "wellknown" must be a string`)
	}
	rawWK := &rawWellKnown{WellKnown: name}
	if p := n.Get("params"); p.Exists() {
		raw, err := p.Raw()
		if err != nil {
			return nil, oops.With("field", "params").Wrapf(err, `query: "params"`)
		}
		rawWK.Params = []byte(raw)
	}
	return rawWK, nil
}

// rawRelation is query.ts's Relation, decoded but not yet resolved against
// pg ; node_resolve.go turns this into a *QueryNode.
type rawRelation struct {
	// Exactly one of Relation/Function is non-empty (IsFunction says which) ;
	// parseRawRelation rejects an empty string for whichever key was given.
	Relation   string
	Function   string
	IsFunction bool

	Schema string // "" : unqualified, resolved via search path
	Alias  string

	On map[string]string // nil if absent

	// Only meaningful when IsFunction ; both nil for a call with no arguments.
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
			return nil, oops.Errorf(`query: relation object must have exactly one of "relation" or "function", not both`)
		}
		return nil, oops.Errorf(`query: relation object needs exactly one of "relation" or "function"`)
	}

	raw := &rawRelation{}
	var err error
	if relNode.Exists() {
		raw.Relation, err = relNode.StrictString()
		if err != nil {
			return nil, oops.With("field", "relation").Wrapf(err, `query: "relation" must be a string`)
		}
		if raw.Relation == "" {
			return nil, oops.Errorf(`query: "relation" must not be empty`)
		}
	} else {
		raw.IsFunction = true
		raw.Function, err = fnNode.StrictString()
		if err != nil {
			return nil, oops.With("field", "function").Wrapf(err, `query: "function" must be a string`)
		}
		if raw.Function == "" {
			return nil, oops.Errorf(`query: "function" must not be empty`)
		}
	}

	if s := n.Get("schema"); s.Exists() {
		raw.Schema, err = s.StrictString()
		if err != nil {
			return nil, oops.With("field", "schema").Wrapf(err, `query: "schema" must be a string`)
		}
	}
	if a := n.Get("alias"); a.Exists() {
		raw.Alias, err = a.StrictString()
		if err != nil {
			return nil, oops.With("field", "alias").Wrapf(err, `query: "alias" must be a string`)
		}
	}

	if on := n.Get("on"); on.Exists() {
		raw.On, err = parseStringMap(on)
		if err != nil {
			return nil, oops.Wrapf(err, `query: "on"`)
		}
	}

	if args := n.Get("arguments"); args.Exists() {
		if !raw.IsFunction {
			return nil, oops.Errorf(`query: "arguments" is only valid alongside "function", not "relation"`)
		}
		switch args.TypeSafe() {
		case ast.V_ARRAY:
			items, err := args.ArrayUseNode()
			if err != nil {
				return nil, oops.Wrapf(err, `query: "arguments"`)
			}
			raw.ArgumentsPositional, err = parseExpressionList(items)
			if err != nil {
				return nil, oops.Wrapf(err, `query: "arguments"`)
			}
		case ast.V_OBJECT:
			raw.ArgumentsNamed, err = parseObjectFields(args)
			if err != nil {
				return nil, oops.Wrapf(err, `query: "arguments"`)
			}
		default:
			return nil, oops.Errorf(`query: "arguments" must be an array or an object`)
		}
	}

	if w := n.Get("where"); w.Exists() {
		raw.Where, err = parseNode(w)
		if err != nil {
			return nil, oops.Wrapf(err, `query: "where"`)
		}
	}

	if wm := n.Get("write_mode"); wm.Exists() {
		raw.WriteMode, err = wm.StrictString()
		if err != nil {
			return nil, oops.With("field", "write_mode").Wrapf(err, `query: "write_mode" must be a string`)
		}
	}

	if oc := n.Get("on_conflict"); oc.Exists() {
		switch oc.TypeSafe() {
		case ast.V_STRING:
			raw.OnConflictConstraintName, err = oc.StrictString()
			if err != nil {
				return nil, oops.Wrapf(err, `query: "on_conflict"`)
			}
		case ast.V_ARRAY:
			raw.OnConflictColumns, err = parseStringListValue(oc)
			if err != nil {
				return nil, oops.Wrapf(err, `query: "on_conflict"`)
			}
		default:
			return nil, oops.Errorf(`query: "on_conflict" must be a string or a string array`)
		}
	}

	if ic := n.Get("insert_columns"); ic.Exists() {
		raw.InsertColumns, err = parseStringListValue(ic)
		if err != nil {
			return nil, oops.Wrapf(err, `query: "insert_columns"`)
		}
	}
	if uc := n.Get("update_columns"); uc.Exists() {
		raw.UpdateColumns, err = parseStringListValue(uc)
		if err != nil {
			return nil, oops.Wrapf(err, `query: "update_columns"`)
		}
	}

	if j := n.Get("join"); j.Exists() {
		fields, err := j.MapUseNode()
		if err != nil {
			return nil, oops.Wrapf(err, `query: "join" must be an object`)
		}
		raw.Join = make(map[string]*rawRelation, len(fields))
		for alias, child := range fields {
			childRaw, err := parseRawRelation(&child)
			if err != nil {
				return nil, oops.With("join_alias", alias).Wrapf(err, "query: join %q", alias)
			}
			raw.Join[alias] = childRaw
		}
	}

	if s := n.Get("select"); s.Exists() {
		raw.Select, err = parseNode(s)
		if err != nil {
			return nil, oops.Wrapf(err, `query: "select"`)
		}
	}

	if d := n.Get("distinct"); d.Exists() {
		raw.Distinct, err = d.StrictBool()
		if err != nil {
			return nil, oops.With("field", "distinct").Wrapf(err, `query: "distinct" must be a boolean`)
		}
	}
	if do := n.Get("distinct_on"); do.Exists() {
		items, err := do.ArrayUseNode()
		if err != nil {
			return nil, oops.With("field", "distinct_on").Wrapf(err, `query: "distinct_on" must be an array`)
		}
		raw.DistinctOn, err = parseExpressionList(items)
		if err != nil {
			return nil, oops.Wrapf(err, `query: "distinct_on"`)
		}
	}

	if ob := n.Get("order_by"); ob.Exists() {
		items, err := ob.ArrayUseNode()
		if err != nil {
			return nil, oops.With("field", "order_by").Wrapf(err, `query: "order_by" must be an array`)
		}
		raw.OrderBy = make([]OrderByTerm, len(items))
		for i := range items {
			term, err := parseOrderByTerm(&items[i])
			if err != nil {
				return nil, oops.With("index", i).Wrapf(err, `query: "order_by" item %d`, i)
			}
			raw.OrderBy[i] = term
		}
	}

	if o := n.Get("offset"); o.Exists() {
		v, err := o.StrictInt64()
		if err != nil {
			return nil, oops.With("field", "offset").Wrapf(err, `query: "offset" must be a number`)
		}
		offset := int(v)
		raw.Offset = &offset
	}
	if l := n.Get("limit"); l.Exists() {
		v, err := l.StrictInt64()
		if err != nil {
			return nil, oops.With("field", "limit").Wrapf(err, `query: "limit" must be a number`)
		}
		limit := int(v)
		raw.Limit = &limit
	}

	return raw, nil
}

// parseOrderByTerm disambiguates a bare Expression from an explicit
// [direction, Expression] pair — only a known direction string counts as the tuple form.
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
		return nil, oops.Wrapf(err, "expected an object")
	}
	out := make(map[string]string, len(fields))
	for k, v := range fields {
		s, err := v.StrictString()
		if err != nil {
			return nil, oops.With("field", k).Wrapf(err, "field %q: expected a string", k)
		}
		out[k] = s
	}
	return out, nil
}
