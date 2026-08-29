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
// specs/query-compiler.md's "Pass 2" and "Identifier resolution" sections.
// Runs bottom-up (children before parents), since a "." chain into a
// child's select needs that child already resolved.
package query

import (
	"github.com/ceymard/rel/pg"
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

	var err error
	if node.Where, err = ctx.resolveExpr(node.Where, node, oc); err != nil {
		return err
	}
	if node.Select, err = ctx.resolveExpr(node.Select, node, oc); err != nil {
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
		// A function node's own Relation (if any) is what its call RETURNS,
		// not something in scope while computing the call's own arguments —
		// those correlate to the enclosing query, same as any subquery's
		// arguments would. A root function call (no parent) has nothing to
		// correlate to : only literals/params are legal there, and a bare
		// Identifier is a hard error (argScope nil).
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

// resolveExpr is the generic entry point : recurses through every Expression
// node type, rebuilding (not relying on in-place mutation) as it goes —
// every caller stores what this returns back into the field it read from.
// n may be nil (root function-call arguments, see ResolveExpressions above)
// — any bare Identifier reached with n == nil is a hard error.
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
			// Opaque JSON operators : Left resolves normally (it may itself
			// be a "." chain landing on a jsonb column), but Right is data
			// (a key, or a path array), never a name to scope-resolve —
			// left untouched.
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

	case ObjectExpr:
		for k, val := range v.Fields {
			resolved, err := ctx.resolveExpr(val, n, oc)
			if err != nil {
				return nil, err
			}
			v.Fields[k] = resolved
		}
		return v, nil

	case OwnExceptExpr:
		if err := ctx.validateExceptColumns(n, v.Except, oc); err != nil {
			return nil, err
		}
		return v, nil

	case FullExceptExpr:
		if err := ctx.validateExceptColumns(n, v.Except, oc); err != nil {
			return nil, err
		}
		return v, nil

	case OwnAndExpr:
		if err := ctx.resolveExprMap(v.And, n, oc); err != nil {
			return nil, err
		}
		return v, nil

	case FullAndExpr:
		if err := ctx.resolveExprMap(v.And, n, oc); err != nil {
			return nil, err
		}
		return v, nil

	case OwnExceptAndExpr:
		if err := ctx.validateExceptColumns(n, v.Except, oc); err != nil {
			return nil, err
		}
		if err := ctx.resolveExprMap(v.And, n, oc); err != nil {
			return nil, err
		}
		return v, nil

	case FullExceptAndExpr:
		if err := ctx.validateExceptColumns(n, v.Except, oc); err != nil {
			return nil, err
		}
		if err := ctx.resolveExprMap(v.And, n, oc); err != nil {
			return nil, err
		}
		return v, nil

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

	case *GetSetExpr:
		col, err := ctx.resolvePlainColumn(n, v.Column, oc)
		if err != nil {
			return nil, err
		}
		v.ResolvedColumn = col
		if v.DefaultGet != nil {
			d, err := ctx.resolveExpr(v.DefaultGet, n, oc)
			if err != nil {
				return nil, err
			}
			v.DefaultGet = d
		}
		if v.DefaultSet != nil {
			d, err := ctx.resolveExpr(v.DefaultSet, n, oc)
			if err != nil {
				return nil, err
			}
			v.DefaultSet = d
		}
		return v, nil

	case *GetExpr:
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
		// Every remaining node type (NullLiteral, BoolLiteral, NumberLiteral,
		// Star, StringLiteral, BigIntLiteral, NumericLiteral, DefaultKeyword,
		// OwnExpr, FullExpr, ParamExpr) has no Expression-typed children and
		// nothing to resolve.
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

func (ctx *ResolveContext) resolveExprMap(exprs map[string]Expression, n *QueryNode, oc oops.OopsErrorBuilder) error {
	for k, v := range exprs {
		resolved, err := ctx.resolveExpr(v, n, oc)
		if err != nil {
			return err
		}
		exprs[k] = resolved
	}
	return nil
}

// resolvePlainColumn resolves a Scope-domain name that may ONLY land on a
// plain physical column of n's own relation — GetExpr/SetExpr/GetSetExpr's
// Column, and (via validateExceptColumns) the Except lists. Never an alias
// or embed, unlike LookupInScope.
func (ctx *ResolveContext) resolvePlainColumn(n *QueryNode, name string, oc oops.OopsErrorBuilder) (*pg.Column, error) {
	if n == nil || n.Relation == nil {
		return nil, oc.Errorf("no relation in scope to resolve column %q", name)
	}
	col := n.Relation.ColumnsMap[name]
	if col == nil {
		return nil, oc.With("column", name).Errorf("unknown column %q", name)
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

// resolveChain resolves e and additionally returns the ResolvedField it
// landed on, for a "." chain's next hop to resolve against. Only *Identifier
// and a "." FoldedExpr carry a landing ; everything else is opaque (nil
// landing) as far as further "." chaining is concerned.
func (ctx *ResolveContext) resolveChain(e Expression, n *QueryNode, oc oops.OopsErrorBuilder) (Expression, ResolvedField, error) {
	switch v := e.(type) {
	case *Identifier:
		if n == nil {
			return nil, nil, oc.Errorf("no scope available to resolve identifier %q", v.Name)
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
				return nil, nil, oc.Errorf("cannot chain \".\" off a JSON-opaque value")
			}
			newRight, hopLanding, err := ctx.resolveHopInto(v.Right, landing, oc)
			if err != nil {
				return nil, nil, err
			}
			v.Right = newRight
			return v, hopLanding, nil
		}
	}

	resolved, err := ctx.resolveExpr(e, n, oc)
	return resolved, nil, err
}

// resolveHopInto resolves `right` (must be an *Identifier — anything else in
// a "." hop position is a hard error) as a hop landing on `into`.
//
// > Question: the *QueryNode case below only reaches physical columns and
// > child aliases via LookupInScope — it does not yet fall back to a
// > computed/renamed key from that node's own select (an ObjectExpr/-and
// > key), which is exactly what the LiteralField variant exists for. How a
// > hop first produces a LiteralField (as opposed to only receiving one via
// > an already-produced chain) was never pinned down in the design pass —
// > flagging as a known, deliberate gap rather than improvising new
// > unreviewed mechanism here.
func (ctx *ResolveContext) resolveHopInto(right Expression, into ResolvedField, oc oops.OopsErrorBuilder) (Expression, ResolvedField, error) {
	ident, ok := right.(*Identifier)
	if !ok {
		return nil, nil, oc.Errorf("a \".\" hop must be a plain name, got %T", right)
	}

	switch land := into.(type) {
	case ColumnPath:
		last := land.Path[len(land.Path)-1]
		if !last.Type.IsComposite() {
			return nil, nil, oc.Errorf("column %q is not composite, cannot chain \".%s\" past it", last.Name, ident.Name)
		}
		col := last.Type.Relation.ColumnsMap[ident.Name]
		if col == nil {
			return nil, nil, oc.Errorf("unknown field %q on composite column %q", ident.Name, last.Name)
		}
		newPath := make([]*pg.Column, len(land.Path)+1)
		copy(newPath, land.Path)
		newPath[len(land.Path)] = col
		result := ColumnPath{Node: land.Node, Path: newPath}
		ident.Resolved = result
		return ident, result, nil

	case *QueryNode:
		field, err := land.LookupInScope(ident.Name)
		if err != nil {
			return nil, nil, err
		}
		ident.Resolved = field
		return ident, field, nil

	case LiteralField:
		childExpr, ok := land.Fields[ident.Name]
		if !ok {
			return nil, nil, oc.Errorf("unknown field %q in literal object", ident.Name)
		}
		resolvedChild, childLanding, err := ctx.resolveChain(childExpr, land.Node, oc)
		if err != nil {
			return nil, nil, err
		}
		land.Fields[ident.Name] = resolvedChild
		ident.Resolved = childLanding
		return ident, childLanding, nil

	default:
		return nil, nil, oc.Errorf("cannot chain \".%s\" further here", ident.Name)
	}
}
