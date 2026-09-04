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
// now-resolved Select only — see specs/query-engine.md's Pass 2 description
// and its "## Writability" section.
package query

import (
	"github.com/ceymard/rel/pg"
	"github.com/samber/oops"
)

// NodeShape is pass 2's derived-shape output for one QueryNode, attached as
// QueryNode.Shape.
type NodeShape struct {
	// Fields is node.Select's resolved Shape (see selectShape,
	// expression_resolve.go) — own/full column expansion, child aliases, and
	// computed (own_and/full_and/object-literal) keys uniformly, at any
	// nesting depth. nil for a computed key whose expression isn't itself
	// chainable (e.g. arithmetic), which is a legitimately opaque terminal,
	// not a gap ; empty entirely if Select isn't a shape-producing
	// expression (e.g. a bare get/get-set or a scalar expression).
	Fields map[string]ResolvedField

	// Extractors is only populated when WriteMode != READONLY — see
	// specs/query-engine.md ## Writability : writability is skipped entirely for reads.
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
func (ctx *ResolveContext) DeriveShapes(root *QueryNode) error {
	for _, c := range root.OutgoingNodes {
		if err := ctx.DeriveShapes(c); err != nil {
			return err
		}
	}
	for _, c := range root.IncomingNodes {
		if err := ctx.DeriveShapes(c); err != nil {
			return err
		}
	}

	oc := oops.With("node", root.InnerName)
	shape, err := ctx.deriveNodeShape(root, oc)
	if err != nil {
		return err
	}
	root.Shape = shape
	return nil
}

func (ctx *ResolveContext) deriveNodeShape(node *QueryNode, oc oops.OopsErrorBuilder) (*NodeShape, error) {
	fields, err := ctx.selectShape(node, oc)
	if err != nil {
		return nil, err
	}
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

// identityIsWritable checks every identity-target column came out writable
// in accum — count exactly 1, clean.
func identityIsWritable(node *QueryNode, accum *writeAccum) bool {
	if node.Relation == nil {
		return false
	}
	// A function-rooted node is never writable (### Function-rooted nodes) ;
	// checked before PrimaryKey/OnConflict so its underlying table can't leak through.
	if node.IsFunction() {
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

// writeAccum tracks, per ColumnPath.Key(), reference count and whether
// every reference so far was "clean" (bare, coalesced, or set/get-set).
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

// walkSelectForWritability descends expr, threading jsonPath and
// coalesceOnly (## Writability) ; get is never recorded, default-value sub-expressions are never walked.
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
		switch v.Op {
		case FoldCoalesceAlias, FoldConcatCoalescing:
			walkSelectForWritability(v.Left, node, jsonPath, coalesceOnly, accum)
			walkSelectForWritability(v.Right, node, jsonPath, coalesceOnly, accum)
		case FoldDot:
			// Record only Right ; cp.Node == node excludes a hop into a
			// child's own column, and an index-hopping chain (see TestResolveExpressions_ArrayIndexThenDot).
			if id, ok := v.Right.(*Identifier); ok && !chainHopsThroughIndex(v) {
				if cp, ok := id.Resolved.(ColumnPath); ok && cp.Node == node {
					accum.record(cp, jsonPath, coalesceOnly)
				}
			}
		default:
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

// chainHopsThroughIndex reports whether e's Left spine passes through an
// ["index", ...] hop ; only Left recurses, Right is always a plain *Identifier.
func chainHopsThroughIndex(e Expression) bool {
	switch v := e.(type) {
	case IndexExpr:
		return true
	case FoldedExpr:
		if v.Op == FoldDot {
			return chainHopsThroughIndex(v.Left)
		}
	}
	return false
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
