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

// Pass 2, resolution step : binds every bare Identifier and every call/agg
// FunctionRef against a scope or the function catalog, in place — see
// specs/query-engine.md's Pass 2 description and its "## Scoping ###
// Identifier resolution" section.
// Runs bottom-up (children before parents), since a "." chain into a
// child's select needs that child already resolved.
package query

import (
	"maps"

	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/pg"
	"github.com/samber/oops"
)

// ResolveExpressions resolves every Expression-typed field of node and its
// entire subtree, bottom-up.
func (ctx *ResolveContext) ResolveExpressions(node *QueryNode) error {
	for _, c := range node.OutgoingNodes {
		if err := ctx.ResolveExpressions(c); err != nil {
			return err
		}
	}
	for _, c := range node.IncomingNodes {
		if err := ctx.ResolveExpressions(c); err != nil {
			return err
		}
	}

	oc := oops.With("node", node.InnerName)

	// Marks node while resolving its own expressions, so resolveExternalHop
	// refuses a self-alias hop into its own still-resolving Shape (## Scoping).
	if ctx.resolvingOwn == nil {
		ctx.resolvingOwn = map[*QueryNode]bool{}
	}
	ctx.resolvingOwn[node] = true
	defer delete(ctx.resolvingOwn, node)

	var err error
	if node.Where, err = ctx.resolveExpr(node.Where, node, oc); err != nil {
		return err
	}
	// selectShape, not resolveExpr : caches the resulting Shape onto
	// node.Shape too, so DeriveShapes (shape.go) gets a cache hit later.
	if _, err = ctx.selectShape(node, oc); err != nil {
		return err
	}
	for i := range node.DistinctOn {
		if node.DistinctOn[i], err = ctx.resolveExpr(node.DistinctOn[i], node, oc); err != nil {
			return err
		}
	}
	for i := range node.OrderBy {
		if node.OrderBy[i].Expr, err = ctx.resolveExpr(node.OrderBy[i].Expr, node, oc); err != nil {
			return err
		}
	}

	if node.IsFunction() {
		// A function's own arguments correlate to the ENCLOSING query, not
		// its own Relation ; a root call (argScope nil) allows only literals/params.
		argScope := node.Parent
		for i := range node.FunctionArguments {
			if node.FunctionArguments[i], err = ctx.resolveExpr(node.FunctionArguments[i], argScope, oc); err != nil {
				return err
			}
		}
		for k, a := range node.FunctionArgumentMap {
			if node.FunctionArgumentMap[k], err = ctx.resolveExpr(a, argScope, oc); err != nil {
				return err
			}
		}
	}

	return nil
}

// resolveExpr recurses through every Expression node type, rebuilding
// (not mutating in place) as it goes ; n nil (root call args) makes any bare Identifier a hard error.
func (ctx *ResolveContext) resolveExpr(e Expression, n *QueryNode, oc oops.OopsErrorBuilder) (Expression, error) {
	if e == nil {
		return nil, nil
	}

	switch v := e.(type) {
	case *Identifier:
		resolved, _, err := ctx.resolveChain(v, n, oc)
		return resolved, err

	case FoldedExpr:
		if v.Op == FoldDot {
			resolved, _, err := ctx.resolveChain(v, n, oc)
			return resolved, err
		}
		if v.Op == FoldJsonGet || v.Op == FoldJsonGetText || v.Op == FoldJsonPathGet || v.Op == FoldJsonPathGetText {
			// Opaque JSON operators : Left resolves normally, but Right is
			// data (a key or path array), never a name — left untouched.
			left, err := ctx.resolveExpr(v.Left, n, oc)
			if err != nil {
				return nil, err
			}
			v.Left = left
			return v, nil
		}
		left, err := ctx.resolveExpr(v.Left, n, oc)
		if err != nil {
			return nil, err
		}
		v.Left = left
		right, err := ctx.resolveExpr(v.Right, n, oc)
		if err != nil {
			return nil, err
		}
		v.Right = right
		return v, nil

	case UnaryExpr:
		expr, err := ctx.resolveExpr(v.Expr, n, oc)
		if err != nil {
			return nil, err
		}
		v.Expr = expr
		return v, nil

	case BinaryExpr:
		left, err := ctx.resolveExpr(v.Left, n, oc)
		if err != nil {
			return nil, err
		}
		v.Left = left
		if v.Op == BinaryCast {
			// "::" : Right is a type name, never a column/alias — left
			// untouched so sql_expr.go's castTypeName reads it directly.
			return v, nil
		}
		right, err := ctx.resolveExpr(v.Right, n, oc)
		if err != nil {
			return nil, err
		}
		v.Right = right
		return v, nil

	case BetweenExpr:
		min, err := ctx.resolveExpr(v.Min, n, oc)
		if err != nil {
			return nil, err
		}
		v.Min = min
		exp, err := ctx.resolveExpr(v.Exp, n, oc)
		if err != nil {
			return nil, err
		}
		v.Exp = exp
		max, err := ctx.resolveExpr(v.Max, n, oc)
		if err != nil {
			return nil, err
		}
		v.Max = max
		return v, nil

	case InExpr:
		subject, err := ctx.resolveExpr(v.Subject, n, oc)
		if err != nil {
			return nil, err
		}
		v.Subject = subject
		for i := range v.Candidates {
			if v.Candidates[i].IsLiteral {
				continue
			}
			expr, err := ctx.resolveExpr(v.Candidates[i].Expr, n, oc)
			if err != nil {
				return nil, err
			}
			v.Candidates[i].Expr = expr
		}
		return v, nil

	case AnyAllExpr:
		subject, err := ctx.resolveExpr(v.Subject, n, oc)
		if err != nil {
			return nil, err
		}
		v.Subject = subject
		arr, err := ctx.resolveExpr(v.Array, n, oc)
		if err != nil {
			return nil, err
		}
		v.Array = arr
		return v, nil

	case ConcatWsExpr:
		sep, err := ctx.resolveExpr(v.Separator, n, oc)
		if err != nil {
			return nil, err
		}
		v.Separator = sep
		if err := ctx.resolveExprSlice(v.Args, n, oc); err != nil {
			return nil, err
		}
		return v, nil

	case CoalesceExpr:
		if err := ctx.resolveExprSlice(v.Args, n, oc); err != nil {
			return nil, err
		}
		return v, nil

	case FormatExpr:
		if err := ctx.resolveExprSlice(v.Args, n, oc); err != nil {
			return nil, err
		}
		return v, nil

	case *AggExpr:
		fn, err := resolveFunctionCandidate(ctx.Db, ctx.Config.Blacklist, v.Identifier.Schema, v.Identifier.Name,
			v.Arguments, nil, (*pg.Function).IsAggregate, oc.With("call", v.Identifier.Name))
		if err != nil {
			return nil, err
		}
		v.ResolvedFunction = fn
		if err := ctx.resolveExprSlice(v.Arguments, n, oc); err != nil {
			return nil, err
		}
		if v.Filter != nil {
			filter, err := ctx.resolveExpr(v.Filter, n, oc)
			if err != nil {
				return nil, err
			}
			v.Filter = filter
		}
		return v, nil

	case *CallExpr:
		fn, err := resolveFunctionCandidate(ctx.Db, ctx.Config.Blacklist, v.Identifier.Schema, v.Identifier.Name,
			v.Arguments, nil, (*pg.Function).IsPlainFunction, oc.With("call", v.Identifier.Name))
		if err != nil {
			return nil, err
		}
		v.ResolvedFunction = fn
		if err := ctx.resolveExprSlice(v.Arguments, n, oc); err != nil {
			return nil, err
		}
		return v, nil

	case ObjectExpr, OwnExpr, FullExpr, OwnExceptExpr, FullExceptExpr, OwnAndExpr, FullAndExpr, OwnExceptAndExpr, FullExceptAndExpr:
		// All shape-producing : delegate to resolveChain (buildShape) ; the
		// landing is discarded, nothing here chains further off it.
		resolved, _, err := ctx.resolveChain(e, n, oc)
		return resolved, err

	case ArrExpr:
		if err := ctx.resolveExprSlice(v.Items, n, oc); err != nil {
			return nil, err
		}
		return v, nil

	case LstExpr:
		if err := ctx.resolveExprSlice(v.Items, n, oc); err != nil {
			return nil, err
		}
		return v, nil

	case IndexExpr:
		arr, err := ctx.resolveExpr(v.Array, n, oc)
		if err != nil {
			return nil, err
		}
		v.Array = arr
		idx, err := ctx.resolveExpr(v.Index, n, oc)
		if err != nil {
			return nil, err
		}
		v.Index = idx
		return v, nil

	case SliceExpr:
		arr, err := ctx.resolveExpr(v.Array, n, oc)
		if err != nil {
			return nil, err
		}
		v.Array = arr
		from, err := ctx.resolveExpr(v.From, n, oc)
		if err != nil {
			return nil, err
		}
		v.From = from
		to, err := ctx.resolveExpr(v.To, n, oc)
		if err != nil {
			return nil, err
		}
		v.To = to
		return v, nil

	case *GetSetExpr, *GetExpr:
		// Both chainable (land directly on a ColumnPath) : delegate to
		// resolveChain, same reasoning as the shape-producing case above.
		resolved, _, err := ctx.resolveChain(e, n, oc)
		return resolved, err

	case *SetExpr:
		col, err := ctx.resolvePlainColumn(n, v.Column, oc)
		if err != nil {
			return nil, err
		}
		v.ResolvedColumn = col
		if v.DefaultValue != nil {
			d, err := ctx.resolveExpr(v.DefaultValue, n, oc)
			if err != nil {
				return nil, err
			}
			v.DefaultValue = d
		}
		return v, nil

	default:
		// Every remaining literal/keyword type has no Expression-typed
		// children and nothing to resolve.
		return e, nil
	}
}

func (ctx *ResolveContext) resolveExprSlice(exprs []Expression, n *QueryNode, oc oops.OopsErrorBuilder) error {
	for i := range exprs {
		resolved, err := ctx.resolveExpr(exprs[i], n, oc)
		if err != nil {
			return err
		}
		exprs[i] = resolved
	}
	return nil
}

// resolvePlainColumn resolves a name that may only land on a plain
// physical column of n's own relation — never an alias/embed, unlike LookupInScope.
func (ctx *ResolveContext) resolvePlainColumn(n *QueryNode, name string, oc oops.OopsErrorBuilder) (*pg.Column, error) {
	if n == nil || n.Relation == nil {
		return nil, oc.Code(errcode.UnknownIdentifier).Errorf("no relation in scope to resolve column %q", name)
	}
	col := n.Relation.ColumnsMap[name]
	if col == nil {
		return nil, oc.With("column", name).Code(errcode.UnknownIdentifier).Errorf("unknown column %q", name)
	}
	return col, nil
}

func (ctx *ResolveContext) validateExceptColumns(n *QueryNode, except []string, oc oops.OopsErrorBuilder) error {
	for _, name := range except {
		if _, err := ctx.resolvePlainColumn(n, name, oc); err != nil {
			return err
		}
	}
	return nil
}

// resolveChain resolves e and also returns the ResolvedField it landed on,
// for a "." chain's next hop ; a non-chainable form lands nil (opaque).
func (ctx *ResolveContext) resolveChain(e Expression, n *QueryNode, oc oops.OopsErrorBuilder) (Expression, ResolvedField, error) {
	switch v := e.(type) {
	case *Identifier:
		if n == nil {
			return nil, nil, oc.Code(errcode.UnknownIdentifier).Errorf("no scope available to resolve identifier %q", v.Name)
		}
		field, err := n.LookupInScope(v.Name)
		if err != nil {
			return nil, nil, err
		}
		v.Resolved = field
		return v, field, nil

	case FoldedExpr:
		if v.Op == FoldDot {
			newLeft, landing, err := ctx.resolveChain(v.Left, n, oc)
			if err != nil {
				return nil, nil, err
			}
			v.Left = newLeft
			if landing == nil {
				return nil, nil, oc.Code(errcode.QueryInvalidExpression).Errorf("cannot chain \".\" off a JSON-opaque value")
			}
			newRight, hopLanding, err := ctx.resolveHopInto(v.Right, landing, oc)
			if err != nil {
				return nil, nil, err
			}
			v.Right = newRight
			return v, hopLanding, nil
		}

	case IndexExpr:
		newArray, arrayLanding, err := ctx.resolveChain(v.Array, n, oc)
		if err != nil {
			return nil, nil, err
		}
		v.Array = newArray
		newIndex, err := ctx.resolveExpr(v.Index, n, oc)
		if err != nil {
			return nil, nil, err
		}
		v.Index = newIndex

		cp, ok := arrayLanding.(ColumnPath)
		if !ok {
			// Indexing something other than a known column path (e.g. a
			// computed array literal) : opaque, nothing further to chain.
			return v, nil, nil
		}
		arrType := cp.CurrentType().Underlying()
		if arrType == nil || !arrType.IsArray() {
			return nil, nil, oc.Code(errcode.QueryInvalidExpression).Errorf("cannot index a non-array value")
		}
		return v, ColumnPath{Node: cp.Node, Path: cp.Path, ElementType: arrType.ElementType}, nil

	case *GetSetExpr:
		col, err := ctx.resolvePlainColumn(n, v.Column, oc)
		if err != nil {
			return nil, nil, err
		}
		v.ResolvedColumn = col
		if v.DefaultGet != nil {
			d, err := ctx.resolveExpr(v.DefaultGet, n, oc)
			if err != nil {
				return nil, nil, err
			}
			v.DefaultGet = d
		}
		if v.DefaultSet != nil {
			d, err := ctx.resolveExpr(v.DefaultSet, n, oc)
			if err != nil {
				return nil, nil, err
			}
			v.DefaultSet = d
		}
		return v, ColumnPath{Node: n, Path: []*pg.Column{col}}, nil

	case *GetExpr:
		col, err := ctx.resolvePlainColumn(n, v.Column, oc)
		if err != nil {
			return nil, nil, err
		}
		v.ResolvedColumn = col
		if v.DefaultValue != nil {
			d, err := ctx.resolveExpr(v.DefaultValue, n, oc)
			if err != nil {
				return nil, nil, err
			}
			v.DefaultValue = d
		}
		return v, ColumnPath{Node: n, Path: []*pg.Column{col}}, nil

	case ObjectExpr:
		shape, err := ctx.buildShape(n, nil, v.Fields, oc)
		return v, shape, err

	case OwnExpr:
		shape, err := ctx.buildShape(n, ownFullBase(n, nil, false), nil, oc)
		return v, shape, err

	case FullExpr:
		shape, err := ctx.buildShape(n, ownFullBase(n, nil, true), nil, oc)
		return v, shape, err

	case OwnExceptExpr:
		if err := ctx.validateExceptColumns(n, v.Except, oc); err != nil {
			return nil, nil, err
		}
		shape, err := ctx.buildShape(n, ownFullBase(n, v.Except, false), nil, oc)
		return v, shape, err

	case FullExceptExpr:
		if err := ctx.validateExceptColumns(n, v.Except, oc); err != nil {
			return nil, nil, err
		}
		shape, err := ctx.buildShape(n, ownFullBase(n, v.Except, true), nil, oc)
		return v, shape, err

	case OwnAndExpr:
		shape, err := ctx.buildShape(n, ownFullBase(n, nil, false), v.And, oc)
		return v, shape, err

	case FullAndExpr:
		shape, err := ctx.buildShape(n, ownFullBase(n, nil, true), v.And, oc)
		return v, shape, err

	case OwnExceptAndExpr:
		if err := ctx.validateExceptColumns(n, v.Except, oc); err != nil {
			return nil, nil, err
		}
		shape, err := ctx.buildShape(n, ownFullBase(n, v.Except, false), v.And, oc)
		return v, shape, err

	case FullExceptAndExpr:
		if err := ctx.validateExceptColumns(n, v.Except, oc); err != nil {
			return nil, nil, err
		}
		shape, err := ctx.buildShape(n, ownFullBase(n, v.Except, true), v.And, oc)
		return v, shape, err
	}

	resolved, err := ctx.resolveExpr(e, n, oc)
	return resolved, nil, err
}

// ownFullBase builds an own/full family Shape's physical-column (and, for
// "full", child-alias) portion — everything that isn't a computed "and" key.
func ownFullBase(n *QueryNode, except []string, includeAliases bool) map[string]ResolvedField {
	base := map[string]ResolvedField{}
	if n.Relation != nil {
		excluded := make(map[string]bool, len(except))
		for _, e := range except {
			excluded[e] = true
		}
		for _, c := range n.Relation.Columns {
			if !excluded[c.Name] {
				base[c.Name] = ColumnPath{Node: n, Path: []*pg.Column{c}}
			}
		}
	}
	if includeAliases {
		for _, c := range n.OutgoingNodes {
			base[c.OuterAlias] = c
		}
		for _, c := range n.IncomingNodes {
			base[c.OuterAlias] = c
		}
	}
	return base
}

// buildShape resolves and's values, merging into base ; a name colliding
// between the two is a hard error, never a silent overwrite.
func (ctx *ResolveContext) buildShape(n *QueryNode, base map[string]ResolvedField, and map[string]Expression, oc oops.OopsErrorBuilder) (Shape, error) {
	shape := make(Shape, len(base)+len(and))
	maps.Copy(shape, base)
	for k, expr := range and {
		if _, exists := shape[k]; exists {
			return nil, oc.Code(errcode.QueryInvalidExpression).Errorf("computed key %q collides with an own/full column or alias of the same name", k)
		}
		resolved, landing, err := ctx.resolveChain(expr, n, oc)
		if err != nil {
			return nil, err
		}
		and[k] = resolved
		shape[k] = landing
	}
	return shape, nil
}

// resolveHopInto resolves `right` as a hop landing on `into`, to-many
// included — the "single row" restriction is compileScalarHop's, not resolution's.
func (ctx *ResolveContext) resolveHopInto(right Expression, into ResolvedField, oc oops.OopsErrorBuilder) (Expression, ResolvedField, error) {
	ident, ok := right.(*Identifier)
	if !ok {
		return nil, nil, oc.Code(errcode.QueryInvalidExpression).Errorf("a \".\" hop must be a plain name, got %T", right)
	}

	switch land := into.(type) {
	case ColumnPath:
		last := land.Path[len(land.Path)-1]
		// CurrentType, not last.Type, directly : it accounts for a domain's
		// base type and an ["index", ...] hop's element-type override.
		rel := land.CurrentType().CompositeRelation()
		if rel == nil {
			return nil, nil, oc.Code(errcode.QueryInvalidExpression).Errorf("column %q is not composite, cannot chain \".%s\" past it", last.Name, ident.Name)
		}
		col := rel.ColumnsMap[ident.Name]
		if col == nil {
			return nil, nil, oc.Code(errcode.UnknownIdentifier).Errorf("unknown field %q on composite column %q", ident.Name, last.Name)
		}
		newPath := make([]*pg.Column, len(land.Path)+1)
		copy(newPath, land.Path)
		newPath[len(land.Path)] = col
		result := ColumnPath{Node: land.Node, Path: newPath}
		ident.Resolved = result
		return ident, result, nil

	case *QueryNode:
		field, err := ctx.resolveExternalHop(land, ident.Name, oc)
		if err != nil {
			return nil, nil, err
		}
		ident.Resolved = field
		return ident, field, nil

	case Shape:
		field, ok := land[ident.Name]
		if !ok {
			return nil, nil, oc.Code(errcode.UnknownIdentifier).Errorf("unknown field %q", ident.Name)
		}
		ident.Resolved = field
		return ident, field, nil

	default:
		return nil, nil, oc.Code(errcode.QueryInvalidExpression).Errorf("cannot chain \".%s\" further here", ident.Name)
	}
}

// resolveExternalHop resolves name as a hop INTO target — unlike a first
// hop, this also reaches target's Shape (never while it's still resolving).
func (ctx *ResolveContext) resolveExternalHop(target *QueryNode, name string, oc oops.OopsErrorBuilder) (ResolvedField, error) {
	scopeField, scopeErr := target.LookupInScope(name)
	scopeOK := scopeErr == nil

	var shape Shape
	if !ctx.resolvingOwn[target] {
		var err error
		shape, err = ctx.selectShape(target, oc)
		if err != nil {
			return nil, err
		}
	}
	shapeField, shapeOK := shape[name]

	switch {
	case scopeOK && shapeOK:
		if resolvedFieldsEqual(scopeField, shapeField) {
			return scopeField, nil
		}
		return nil, oc.Code(errcode.UnknownIdentifier).Errorf("identifier %q is ambiguous : scope and the select shape disagree on what it means", name)
	case scopeOK:
		return scopeField, nil
	case shapeOK:
		return shapeField, nil
	default:
		// Neither found ; scopeErr may itself be "ambiguous" rather than
		// "not found" — either way it's the right error to surface.
		return nil, scopeErr
	}
}

// resolvedFieldsEqual tells resolveExternalHop's "found the same thing
// twice" (fine) from "found two different things under one name" (an error).
func resolvedFieldsEqual(a, b ResolvedField) bool {
	switch av := a.(type) {
	case ColumnPath:
		bv, ok := b.(ColumnPath)
		return ok && av.Key() == bv.Key()
	case *QueryNode:
		bv, ok := b.(*QueryNode)
		return ok && av == bv
	case ComputedFieldRef:
		bv, ok := b.(ComputedFieldRef)
		return ok && av.Node == bv.Node && av.Function == bv.Function
	default:
		return false
	}
}

// selectShape returns target's exported Shape, memoized on target.Shape.Fields ;
// ctx.shapeInProgress guards the one unsafe case, a self-reference.
func (ctx *ResolveContext) selectShape(target *QueryNode, oc oops.OopsErrorBuilder) (Shape, error) {
	if target.Shape != nil {
		return target.Shape.Fields, nil
	}
	if ctx.shapeInProgress[target] {
		return nil, oc.Code(errcode.QueryInvalidExpression).Errorf("a node's select cannot reference itself through its own shape (self-referential \".\" chain)")
	}
	if ctx.shapeInProgress == nil {
		ctx.shapeInProgress = map[*QueryNode]bool{}
	}
	ctx.shapeInProgress[target] = true
	defer delete(ctx.shapeInProgress, target)

	newSelect, landing, err := ctx.resolveChain(target.Select, target, oc)
	if err != nil {
		return nil, err
	}
	target.Select = newSelect
	shape, _ := landing.(Shape) // nil (empty) if Select isn't shape-producing — not an error
	if shape == nil {
		shape = Shape{}
	}
	target.Shape = &NodeShape{Fields: shape}
	return shape, nil
}
