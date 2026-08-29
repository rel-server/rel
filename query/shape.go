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

// Pass 2, shape/writability derivation step : runs after ResolveExpressions
// (expression_resolve.go) has resolved every identifier, over each node's
// now-resolved Select only — see specs/query-compiler.md's "Pass 2" section,
// step 2.
package query

import "github.com/ceymard/rel/pg"

// NodeShape is pass 2's derived-shape output for one QueryNode, attached as
// QueryNode.Shape.
type NodeShape struct {
	// Fields maps this node's exported select keys to what they resolve to
	// — best-effort : own/full column expansion and get/get-set/object-
	// literal keys are fully typed (ColumnPath/*QueryNode), a computed
	// (own-and/full-and) key whose expression isn't itself a directly
	// resolved identifier is left nil (opaque ; nothing needs it to be more
	// than that for writability/extraction purposes, and dot-chaining into
	// a child's computed keys isn't implemented yet — see
	// expression_resolve.go's resolveHopInto doc comment).
	Fields map[string]ResolvedField

	// Extractors is only populated when WriteMode != READONLY — see
	// query-compiler.md : writability is skipped entirely for reads.
	Extractors []Extractor

	// Writable is this node's own identity-target columns (PK, or
	// on_conflict's columns) all coming out writable by the exactly-once/
	// coalescing-only rule, AND (propagated) every child whose own
	// WriteMode isn't READONLY is itself Writable. A node whose own
	// WriteMode is READONLY is trivially Writable (nothing to check).
	Writable bool
}

// Extractor records where one writable column's value lives within a data
// payload shaped like this node's select : Path is the physical column (or
// composite sub-field, independently writable per this session's decision),
// JsonPath is the sequence of select-output keys leading to it.
type Extractor struct {
	Path     ColumnPath
	JsonPath []string
}

// DeriveShapes computes NodeShape for root and its entire subtree,
// bottom-up (children before parent — tree-level writability propagation
// needs each child's Shape already computed).
func DeriveShapes(root *QueryNode) error {
	for _, c := range root.OutgoingNodes {
		if err := DeriveShapes(c); err != nil {
			return err
		}
	}
	for _, c := range root.IncomingNodes {
		if err := DeriveShapes(c); err != nil {
			return err
		}
	}

	shape, err := deriveNodeShape(root)
	if err != nil {
		return err
	}
	root.Shape = shape
	return nil
}

func deriveNodeShape(node *QueryNode) (*NodeShape, error) {
	fields := deriveExportedFields(node)
	shape := &NodeShape{Fields: fields, Writable: true}

	if node.WriteMode == READONLY {
		return shape, nil
	}

	accum := &writeAccum{
		occurrences: map[string]int{},
		clean:       map[string]bool{},
		paths:       map[string][]string{},
		columns:     map[string]ColumnPath{},
	}
	walkSelectForWritability(node.Select, node, nil, true, accum)

	for key, count := range accum.occurrences {
		if count == 1 && accum.clean[key] {
			shape.Extractors = append(shape.Extractors, Extractor{
				Path:     accum.columns[key],
				JsonPath: accum.paths[key],
			})
		}
	}

	shape.Writable = identityIsWritable(node, accum)

	for _, c := range node.OutgoingNodes {
		if c.WriteMode != READONLY && (c.Shape == nil || !c.Shape.Writable) {
			shape.Writable = false
		}
	}
	for _, c := range node.IncomingNodes {
		if c.WriteMode != READONLY && (c.Shape == nil || !c.Shape.Writable) {
			shape.Writable = false
		}
	}

	return shape, nil
}

// identityIsWritable checks that every identity-target column (on_conflict's
// columns if set, else the primary key's) came out writable — count exactly
// 1, clean — in accum.
func identityIsWritable(node *QueryNode, accum *writeAccum) bool {
	if node.Relation == nil {
		return false
	}

	var identityCols []*pg.Column
	if len(node.OnConflictColumns) > 0 {
		for _, name := range node.OnConflictColumns {
			if c := node.Relation.ColumnsMap[name]; c != nil {
				identityCols = append(identityCols, c)
			}
		}
	} else if node.Relation.PrimaryKey != nil {
		identityCols = node.Relation.PrimaryKey.Columns
	}
	if len(identityCols) == 0 {
		return false
	}

	for _, c := range identityCols {
		key := (ColumnPath{Node: node, Path: []*pg.Column{c}}).Key()
		if accum.occurrences[key] != 1 || !accum.clean[key] {
			return false
		}
	}
	return true
}

// deriveExportedFields walks node's (already-resolved) Select structurally
// to determine what named keys it exports and what each resolves to.
func deriveExportedFields(node *QueryNode) map[string]ResolvedField {
	fields := map[string]ResolvedField{}
	switch v := node.Select.(type) {
	case OwnExpr:
		addOwnFields(node, nil, fields)
	case FullExpr:
		addOwnFields(node, nil, fields)
		addChildAliasFields(node, fields)
	case OwnExceptExpr:
		addOwnFields(node, v.Except, fields)
	case FullExceptExpr:
		addOwnFields(node, v.Except, fields)
		addChildAliasFields(node, fields)
	case OwnAndExpr:
		addOwnFields(node, nil, fields)
		addComputedFields(v.And, fields)
	case FullAndExpr:
		addOwnFields(node, nil, fields)
		addChildAliasFields(node, fields)
		addComputedFields(v.And, fields)
	case OwnExceptAndExpr:
		addOwnFields(node, v.Except, fields)
		addComputedFields(v.And, fields)
	case FullExceptAndExpr:
		addOwnFields(node, v.Except, fields)
		addChildAliasFields(node, fields)
		addComputedFields(v.And, fields)
	case ObjectExpr:
		addComputedFields(v.Fields, fields)
		return fields
	case *GetSetExpr:
		fields[v.Column] = ColumnPath{Node: node, Path: []*pg.Column{v.ResolvedColumn}}
		return fields
	case *GetExpr:
		fields[v.Column] = ColumnPath{Node: node, Path: []*pg.Column{v.ResolvedColumn}}
		return fields
	default:
		// A scalar select (a single expression, no keyed shape) or a form
		// not yet handled here : no named fields to export.
		return map[string]ResolvedField{}
	}
	return fields
}

func addOwnFields(node *QueryNode, except []string, fields map[string]ResolvedField) {
	if node.Relation == nil {
		return
	}
	excluded := map[string]bool{}
	for _, e := range except {
		excluded[e] = true
	}
	for _, c := range node.Relation.Columns {
		if excluded[c.Name] {
			continue
		}
		fields[c.Name] = ColumnPath{Node: node, Path: []*pg.Column{c}}
	}
}

func addChildAliasFields(node *QueryNode, fields map[string]ResolvedField) {
	for _, c := range node.OutgoingNodes {
		fields[c.OuterAlias] = c
	}
	for _, c := range node.IncomingNodes {
		fields[c.OuterAlias] = c
	}
}

func addComputedFields(and map[string]Expression, fields map[string]ResolvedField) {
	for k, expr := range and {
		if id, ok := expr.(*Identifier); ok {
			fields[k] = id.Resolved
		} else {
			fields[k] = nil // opaque : a computed expression, not a direct reference
		}
	}
}

// writeAccum tracks, per ColumnPath (keyed by ColumnPath.Key(), since
// composite sub-fields are independently writable and []*pg.Column isn't a
// valid map key), how many times it was referenced in Select and whether
// every reference so far was "clean" (bare, or wrapped only by coalescing
// operators, or via set/get-set — never get, which is excluded entirely).
type writeAccum struct {
	occurrences map[string]int
	clean       map[string]bool
	paths       map[string][]string // first-seen JsonPath per key
	columns     map[string]ColumnPath
}

func (a *writeAccum) record(path ColumnPath, jsonPath []string, isClean bool) {
	key := path.Key()
	if _, seen := a.occurrences[key]; !seen {
		a.clean[key] = true
		pathCopy := make([]string, len(jsonPath))
		copy(pathCopy, jsonPath)
		a.paths[key] = pathCopy
		a.columns[key] = path
	}
	a.occurrences[key]++
	if !isClean {
		a.clean[key] = false
	}
}

// walkSelectForWritability descends through expr, threading jsonPath (the
// select-output key path so far) and coalesceOnly (whether every wrapper
// seen since the start of the CURRENT field was a coalescing operator — see
// query-compiler.md's writability rule). get's Column is never recorded at
// all (read-only, excluded entirely) ; get-set's/set's Column is recorded at
// the CURRENT jsonPath (the output key this get-set/set sits at), which may
// differ from Column's own name when nested under a renaming key. Default-
// value sub-expressions (get's DefaultValue, get-set's DefaultGet/
// DefaultSet) are never walked — a default expression supplies a fallback
// value, it doesn't claim the columns it reads as its own write target.
func walkSelectForWritability(expr Expression, node *QueryNode, jsonPath []string, coalesceOnly bool, accum *writeAccum) {
	if expr == nil {
		return
	}

	switch v := expr.(type) {
	case *Identifier:
		if cp, ok := v.Resolved.(ColumnPath); ok {
			accum.record(cp, jsonPath, coalesceOnly)
		}

	case *GetSetExpr:
		if v.ResolvedColumn != nil {
			accum.record(ColumnPath{Node: node, Path: []*pg.Column{v.ResolvedColumn}}, jsonPath, coalesceOnly)
		}

	case *SetExpr:
		if v.ResolvedColumn != nil {
			accum.record(ColumnPath{Node: node, Path: []*pg.Column{v.ResolvedColumn}}, jsonPath, coalesceOnly)
		}

	case *GetExpr:
		// excluded entirely — read-only, never a write occurrence, and its
		// DefaultValue is not walked either.

	case CoalesceExpr:
		for _, a := range v.Args {
			walkSelectForWritability(a, node, jsonPath, coalesceOnly, accum)
		}

	case FoldedExpr:
		if v.Op == FoldCoalesceAlias || v.Op == FoldConcatCoalescing {
			walkSelectForWritability(v.Left, node, jsonPath, coalesceOnly, accum)
			walkSelectForWritability(v.Right, node, jsonPath, coalesceOnly, accum)
		} else {
			walkSelectForWritability(v.Left, node, jsonPath, false, accum)
			walkSelectForWritability(v.Right, node, jsonPath, false, accum)
		}

	case UnaryExpr:
		walkSelectForWritability(v.Expr, node, jsonPath, false, accum)

	case BinaryExpr:
		walkSelectForWritability(v.Left, node, jsonPath, false, accum)
		walkSelectForWritability(v.Right, node, jsonPath, false, accum)

	case BetweenExpr:
		walkSelectForWritability(v.Min, node, jsonPath, false, accum)
		walkSelectForWritability(v.Exp, node, jsonPath, false, accum)
		walkSelectForWritability(v.Max, node, jsonPath, false, accum)

	case InExpr:
		walkSelectForWritability(v.Subject, node, jsonPath, false, accum)
		for _, c := range v.Candidates {
			if !c.IsLiteral {
				walkSelectForWritability(c.Expr, node, jsonPath, false, accum)
			}
		}

	case AnyAllExpr:
		walkSelectForWritability(v.Subject, node, jsonPath, false, accum)
		walkSelectForWritability(v.Array, node, jsonPath, false, accum)

	case ConcatWsExpr:
		walkSelectForWritability(v.Separator, node, jsonPath, false, accum)
		for _, a := range v.Args {
			walkSelectForWritability(a, node, jsonPath, false, accum)
		}

	case FormatExpr:
		for _, a := range v.Args {
			walkSelectForWritability(a, node, jsonPath, false, accum)
		}

	case *AggExpr:
		for _, a := range v.Arguments {
			walkSelectForWritability(a, node, jsonPath, false, accum)
		}
		walkSelectForWritability(v.Filter, node, jsonPath, false, accum)

	case *CallExpr:
		for _, a := range v.Arguments {
			walkSelectForWritability(a, node, jsonPath, false, accum)
		}

	case ObjectExpr:
		for k, val := range v.Fields {
			walkSelectForWritability(val, node, append(jsonPath, k), true, accum)
		}

	case OwnExpr:
		recordOwnColumns(node, nil, jsonPath, accum)

	case FullExpr:
		recordOwnColumns(node, nil, jsonPath, accum)

	case OwnExceptExpr:
		recordOwnColumns(node, v.Except, jsonPath, accum)

	case FullExceptExpr:
		recordOwnColumns(node, v.Except, jsonPath, accum)

	case OwnAndExpr:
		recordOwnColumns(node, nil, jsonPath, accum)
		for k, val := range v.And {
			walkSelectForWritability(val, node, append(jsonPath, k), true, accum)
		}

	case FullAndExpr:
		recordOwnColumns(node, nil, jsonPath, accum)
		for k, val := range v.And {
			walkSelectForWritability(val, node, append(jsonPath, k), true, accum)
		}

	case OwnExceptAndExpr:
		recordOwnColumns(node, v.Except, jsonPath, accum)
		for k, val := range v.And {
			walkSelectForWritability(val, node, append(jsonPath, k), true, accum)
		}

	case FullExceptAndExpr:
		recordOwnColumns(node, v.Except, jsonPath, accum)
		for k, val := range v.And {
			walkSelectForWritability(val, node, append(jsonPath, k), true, accum)
		}

	case ArrExpr:
		for _, a := range v.Items {
			walkSelectForWritability(a, node, jsonPath, false, accum)
		}

	case LstExpr:
		for _, a := range v.Items {
			walkSelectForWritability(a, node, jsonPath, false, accum)
		}

	case IndexExpr:
		walkSelectForWritability(v.Array, node, jsonPath, false, accum)

	case SliceExpr:
		walkSelectForWritability(v.Array, node, jsonPath, false, accum)

	default:
		// literals, Star, ParamExpr, DefaultKeyword, *QueryNode-typed
		// embeds reached indirectly, etc. : nothing to record.
	}
}

func recordOwnColumns(node *QueryNode, except []string, jsonPath []string, accum *writeAccum) {
	if node.Relation == nil {
		return
	}
	excluded := map[string]bool{}
	for _, e := range except {
		excluded[e] = true
	}
	for _, c := range node.Relation.Columns {
		if excluded[c.Name] {
			continue
		}
		accum.record(ColumnPath{Node: node, Path: []*pg.Column{c}}, append(jsonPath, c.Name), true)
	}
}
